package approve

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/adapters/hh"
	"github.com/Darkon13/job-agent/buildinfo"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
	"github.com/Darkon13/job-agent/workflow"
)

func runMain() {
	if buildinfo.Requested(os.Args[1:]) {
		if err := buildinfo.Write("job-agent-approve", os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := run(context.Background(), os.Args[1:], os.Stdout, time.Now().UTC()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string, output io.Writer, now time.Time) error {
	flags := flag.NewFlagSet("job-agent-approve", flag.ContinueOnError)
	flags.SetOutput(output)
	idempotencyKey := flags.String("idempotency-key", "", "stable key for this approval")
	if err := flags.Parse(args); err != nil {
		return err
	}
	positional := flags.Args()
	if len(positional) != 3 || strings.TrimSpace(*idempotencyKey) == "" {
		return errors.New("usage: job-agent-approve -idempotency-key KEY <config.json> <profile> <vacancy-id>")
	}
	cfg, err := appconfig.Load(positional[0])
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	profile, platform, err := resolveProfilePlatform(cfg, positional[1])
	if err != nil {
		return err
	}
	if profile.Applications.ExecutionMode() != appconfig.ApplicationModeSubmit {
		return fmt.Errorf("profile %q must use application mode submit before approval is released", profile.Tag)
	}

	store, err := storesqlite.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer store.Close()
	key := core.ApplicationKey{
		ProfileID: core.ProfileID(profile.Tag),
		Vacancy:   core.VacancyKey{Platform: platform, ExternalID: positional[2]},
	}
	application, err := store.Application(ctx, key)
	if err != nil {
		return fmt.Errorf("application: %w", err)
	}
	if strings.TrimSpace(application.PreparedMessage) == "" {
		return errors.New("application approval requires a prepared cover letter")
	}
	switch application.Status {
	case core.ApplicationWaitingApproval:
		if err := application.Transition(core.ApplicationReady, now); err != nil {
			return err
		}
		if err := store.SaveApplication(ctx, application, core.ApplicationWaitingApproval); err != nil {
			return fmt.Errorf("release application approval: %w", err)
		}
	case core.ApplicationReady:
		// A previous invocation may have saved the state before enqueue failed.
	default:
		return fmt.Errorf("application cannot be approved from status %s", application.Status)
	}

	payload, err := json.Marshal(core.ApplicationSubmitPayload{ApplicationID: application.ID, Key: application.Key})
	if err != nil {
		return err
	}
	ids := workflow.RandomIDGenerator{}
	taskID, err := ids.NewID("task")
	if err != nil {
		return err
	}
	correlationID, err := ids.NewID("correlation")
	if err != nil {
		return err
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(taskID), Type: core.TaskApplicationSubmit, IdempotencyKey: *idempotencyKey,
		Source: "manual:application.approve", Platform: platform, ProfileID: core.ProfileID(profile.Tag),
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
	}, now)
	if err != nil {
		return err
	}
	created, err := store.Enqueue(ctx, task)
	if err != nil {
		return fmt.Errorf("enqueue approved application: %w", err)
	}
	stored, err := store.TaskByIdempotencyKey(ctx, *idempotencyKey)
	if err != nil {
		return fmt.Errorf("load approved application task: %w", err)
	}
	fmt.Fprintf(output, "OK application=%s status=%s task=%s created=%t\n", application.ID, application.Status, stored.ID, created)
	return nil
}

func resolveProfilePlatform(cfg appconfig.Config, profileTag string) (appconfig.Profile, core.Platform, error) {
	var profile appconfig.Profile
	for _, item := range cfg.Profiles {
		if item.Tag == profileTag {
			profile = item
			break
		}
	}
	if profile.Tag == "" || !profile.Enabled {
		return appconfig.Profile{}, "", fmt.Errorf("profile %q is not enabled", profileTag)
	}
	var configured appconfig.AdapterConfig
	for _, item := range cfg.Adapters {
		if item.Tag == profile.Adapter {
			configured = item
			break
		}
	}
	if configured.Tag == "" {
		return appconfig.Profile{}, "", fmt.Errorf("profile %q references an unknown adapter", profileTag)
	}
	registry := adapter.NewRegistry()
	if err := registry.Register(hh.Name, hh.New); err != nil {
		return appconfig.Profile{}, "", err
	}
	instance, err := registry.Open(configured.Type, configured.Settings)
	if err != nil {
		return appconfig.Profile{}, "", err
	}
	return profile, core.Platform(instance.Name()), nil
}

// Run executes the tool with the given arguments and returns a process exit
// code. It preserves the historical CLI contract of cmd/job-agent-approve.
func Run(args []string) int {
	os.Args = append([]string{os.Args[0]}, args...)
	runMain()
	return 0
}
