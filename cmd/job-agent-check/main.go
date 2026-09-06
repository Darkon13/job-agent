package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/adapters/hh"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

type profileReadiness struct {
	apiReady     bool
	browserReady bool
}

type browserState struct {
	Cookies []json.RawMessage `json:"cookies"`
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: job-agent-check <config.json>")
	}
	cfg, err := appconfig.Load(args[0])
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	fmt.Fprintln(output, "OK config")

	store, err := storesqlite.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer store.Close()
	stats, err := store.Stats(ctx)
	if err != nil {
		return fmt.Errorf("database stats: %w", err)
	}
	fmt.Fprintf(output, "OK database schema=%d vacancies=%d applications=%d campaigns=%d tasks=%d\n",
		storesqlite.LatestSchemaVersion, stats.Vacancies, stats.Applications, stats.ApplicationCampaigns, stats.Tasks)
	taskCounts, err := store.TaskCounts(ctx)
	if err != nil {
		return fmt.Errorf("database task summary: %w", err)
	}
	for _, item := range taskCounts {
		fmt.Fprintf(output, "INFO queue type=%s status=%s count=%d\n", item.Type, item.Status, item.Count)
	}
	applicationCounts, err := store.ApplicationCounts(ctx)
	if err != nil {
		return fmt.Errorf("database application summary: %w", err)
	}
	for _, item := range applicationCounts {
		fmt.Fprintf(output, "INFO applications status=%s decision=%s count=%d\n", item.Status, emptyAs(item.DecisionCode, "none"), item.Count)
	}
	dueSchedules, err := store.DueSchedules(ctx, time.Now().UTC(), 100)
	if err != nil {
		return fmt.Errorf("database due schedules: %w", err)
	}
	for _, entry := range dueSchedules {
		fmt.Fprintf(output, "WARN persisted_schedule=%s/%d due=true action=%s\n",
			entry.JobTag, entry.TriggerIndex, entry.ActionType)
	}

	registry := adapter.NewRegistry()
	if err := registry.Register(hh.Name, hh.New); err != nil {
		return err
	}
	instances := make(map[string]adapter.Adapter, len(cfg.Adapters))
	for _, configured := range cfg.Adapters {
		instance, err := registry.Open(configured.Type, configured.Settings)
		if err != nil {
			return fmt.Errorf("adapter %q: %w", configured.Tag, err)
		}
		instances[configured.Tag] = instance
	}
	for _, search := range cfg.Searches {
		if err := instances[search.Adapter].ValidateSearch(search.Query); err != nil {
			return fmt.Errorf("search %q: %w", search.Tag, err)
		}
	}
	fmt.Fprintf(output, "OK adapters=%d searches_valid=%d\n", len(instances), len(cfg.Searches))

	readiness := make(map[string]profileReadiness, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		state := profileReadiness{}
		if !profile.Enabled {
			fmt.Fprintf(output, "SKIP profile=%s disabled\n", profile.Tag)
			readiness[profile.Tag] = state
			continue
		}
		if profile.CredentialsRef != "" {
			factory, ok := instances[profile.Adapter].(adapter.ProfileReaderFactory)
			if !ok {
				return fmt.Errorf("profile %q: adapter does not support authenticated reads", profile.Tag)
			}
			reader, err := factory.NewProfileReader(core.ProfileID(profile.Tag), profile.CredentialsRef)
			if err != nil {
				return fmt.Errorf("profile %q credentials: %w", profile.Tag, err)
			}
			probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, err = reader.ReadProfile(probeCtx, core.ProfileID(profile.Tag))
			cancel()
			if core.ErrorIsCategory(err, core.ErrorUnauthorized) {
				fmt.Fprintf(output, "WARN profile=%s api=auth_required\n", profile.Tag)
			} else if err != nil {
				return fmt.Errorf("profile %q API probe: %w", profile.Tag, err)
			} else {
				state.apiReady = true
				fmt.Fprintf(output, "OK profile=%s api=authorized\n", profile.Tag)
			}
		} else {
			fmt.Fprintf(output, "INFO profile=%s api=not_configured\n", profile.Tag)
		}
		if profile.StateFile != "" {
			if err := validateBrowserState(profile.StateFile, instances[profile.Adapter].Name()); err != nil {
				fmt.Fprintf(output, "WARN profile=%s browser_state=%s\n", profile.Tag, err)
			} else {
				state.browserReady = true
				fmt.Fprintf(output, "OK profile=%s browser_state=present\n", profile.Tag)
			}
		}
		readiness[profile.Tag] = state
	}

	blocked := make([]string, 0)
	runnableSearches := 0
	for _, search := range cfg.Searches {
		runnable := false
		for _, profile := range search.Profiles {
			configured := configuredProfile(cfg.Profiles, profile)
			if profileCanRead(configured, readiness[profile], instances[configured.Adapter]) {
				runnable = true
				break
			}
		}
		if runnable {
			runnableSearches++
		} else {
			blocked = append(blocked, "search "+search.Tag+" has no API or browser read profile")
		}
	}

	enabledJobs := 0
	runnableJobs := 0
	configuredProfiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		configuredProfiles[profile.Tag] = profile
	}
	for _, job := range cfg.Jobs {
		if !job.Enabled {
			continue
		}
		enabledJobs++
		switch job.Action.Type {
		case appconfig.JobActionResumeTouch:
			profile := configuredProfiles[job.Action.Profile]
			if !readiness[job.Action.Profile].browserReady {
				blocked = append(blocked, "job "+job.Tag+" has no valid browser state")
				continue
			}
			if instances[profile.Adapter].Name() == hh.Name {
				resumeID := job.Action.Resume
				if resumeID == "" {
					resumeID = profile.Resume
				}
				transport, err := hh.NewResumeTouchTransport(profile.StateFile, nil)
				if err != nil {
					return fmt.Errorf("job %q browser transport: %w", job.Tag, err)
				}
				probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				probe, err := transport.ProbeResume(probeCtx, resumeID)
				cancel()
				if core.ErrorIsCategory(err, core.ErrorUnauthorized) {
					fmt.Fprintf(output, "WARN job=%s browser_session=auth_required\n", job.Tag)
					blocked = append(blocked, "job "+job.Tag+" browser session requires authentication")
					continue
				}
				if err != nil {
					return fmt.Errorf("job %q browser probe: %w", job.Tag, err)
				}
				fmt.Fprintf(output, "OK job=%s resume=found can_touch=%t\n", job.Tag, probe.CanTouch)
			}
			runnableJobs++
		case appconfig.JobActionApplicationCampaign:
			runnable := true
			for _, profile := range job.Action.Profiles {
				configured := configuredProfiles[profile]
				if !profileCanRunApplications(configured, readiness[profile], instances[configured.Adapter]) {
					runnable = false
					break
				}
			}
			if runnable {
				runnableJobs++
			} else {
				blocked = append(blocked, "job "+job.Tag+" has a profile without API or browser read access")
			}
		}
	}

	fmt.Fprintf(output, "SUMMARY service=ready searches=%d/%d jobs=%d/%d persisted_due=%d\n",
		runnableSearches, len(cfg.Searches), runnableJobs, enabledJobs, len(dueSchedules))
	for _, reason := range blocked {
		fmt.Fprintln(output, "BLOCKED "+reason)
	}
	if len(blocked) > 0 {
		return fmt.Errorf("preflight blocked: %d configured operation(s) are not runnable", len(blocked))
	}
	return nil
}

func emptyAs(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func configuredProfile(profiles []appconfig.Profile, tag string) appconfig.Profile {
	for _, profile := range profiles {
		if profile.Tag == tag {
			return profile
		}
	}
	return appconfig.Profile{}
}

func profileCanRead(profile appconfig.Profile, readiness profileReadiness, instance adapter.Adapter) bool {
	if readiness.apiReady {
		return true
	}
	if !readiness.browserReady || instance == nil {
		return false
	}
	_, supported := instance.(adapter.BrowserSessionBinder)
	return supported
}

func profileCanRunApplications(profile appconfig.Profile, readiness profileReadiness, instance adapter.Adapter) bool {
	if readiness.apiReady {
		return true
	}
	if !profileCanRead(profile, readiness, instance) {
		return false
	}
	if profile.Applications.ExecutionMode() == appconfig.ApplicationModeDryRun {
		return true
	}
	_, supported := instance.(adapter.BrowserApplicationSessionBinder)
	return supported
}

func validateBrowserState(path, platform string) error {
	info, err := os.Stat(path)
	if err != nil {
		return errors.New("unavailable")
	}
	if !info.Mode().IsRegular() {
		return errors.New("not_regular")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("insecure_permissions")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("unavailable")
	}
	var state browserState
	if err := json.Unmarshal(data, &state); err != nil {
		return errors.New("invalid_json")
	}
	if len(state.Cookies) == 0 {
		return errors.New("no_cookies")
	}
	matchingPlatformCookie := false
	for _, cookie := range state.Cookies {
		var item struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		}
		if err := json.Unmarshal(cookie, &item); err != nil || strings.TrimSpace(item.Name) == "" || item.Value == "" {
			return errors.New("invalid_cookie")
		}
		if platform != hh.Name || hhBrowserDomain(item.Domain) {
			matchingPlatformCookie = true
		}
	}
	if !matchingPlatformCookie {
		return errors.New("no_platform_cookies")
	}
	return nil
}

func hhBrowserDomain(value string) bool {
	domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
	return domain == "hh.ru" || strings.HasSuffix(domain, ".hh.ru")
}
