package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
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
		if err := buildinfo.Write("job-agent-trigger", os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := run(context.Background(), os.Args[1:], os.Stdout, time.Now().UTC()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string, output io.Writer, now time.Time) error {
	flags := flag.NewFlagSet("job-agent-trigger", flag.ContinueOnError)
	flags.SetOutput(output)
	idempotencyKey := flags.String("idempotency-key", "", "stable key for this manual trigger")
	if err := flags.Parse(args); err != nil {
		return err
	}
	positional := flags.Args()
	if len(positional) != 2 || *idempotencyKey == "" {
		return errors.New("usage: job-agent-trigger -idempotency-key KEY <config.json> <job-tag>")
	}
	cfg, err := appconfig.Load(positional[0])
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	var job *appconfig.Job
	for index := range cfg.Jobs {
		if cfg.Jobs[index].Tag == positional[1] {
			job = &cfg.Jobs[index]
			break
		}
	}
	if job == nil {
		return fmt.Errorf("job %q is not configured", positional[1])
	}
	if !job.Enabled {
		return fmt.Errorf("job %q is disabled", job.Tag)
	}

	registry := adapter.NewRegistry()
	if err := registry.Register(hh.Name, hh.New); err != nil {
		return err
	}
	platforms := make(map[string]core.Platform, len(cfg.Adapters))
	for _, configured := range cfg.Adapters {
		instance, err := registry.Open(configured.Type, configured.Settings)
		if err != nil {
			return fmt.Errorf("adapter %q: %w", configured.Tag, err)
		}
		platforms[configured.Tag] = core.Platform(instance.Name())
	}

	commands, err := commandsForJob(cfg, *job)
	if err != nil {
		return err
	}
	type pendingTask struct {
		task core.Task
		key  string
	}
	ids := workflow.RandomIDGenerator{}
	pending := make([]pendingTask, 0, len(commands))
	for index, command := range commands {
		key := *idempotencyKey
		if len(commands) > 1 {
			key = fmt.Sprintf("%s:%d", key, index)
		}
		taskID, err := ids.NewID("task")
		if err != nil {
			return err
		}
		correlationID, err := ids.NewID("correlation")
		if err != nil {
			return err
		}
		adapterTag, err := adapterForCommand(cfg, *job, command)
		if err != nil {
			return err
		}
		platform := platforms[adapterTag]
		task, err := core.NewTask(core.NewTaskParams{
			ID: core.TaskID(taskID), Type: command.taskType, IdempotencyKey: key,
			Source: "manual:" + job.Tag, Platform: platform, ProfileID: command.profileID,
			CorrelationID: core.CorrelationID(correlationID), Payload: command.payload, Priority: job.Priority,
		}, now)
		if err != nil {
			return err
		}
		pending = append(pending, pendingTask{task: task, key: key})
	}
	store, err := storesqlite.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer store.Close()
	for _, item := range pending {
		created, err := store.Enqueue(ctx, item.task)
		if err != nil {
			return fmt.Errorf("enqueue job %q: %w", job.Tag, err)
		}
		stored, err := store.TaskByIdempotencyKey(ctx, item.key)
		if err != nil {
			return fmt.Errorf("load triggered job %q: %w", job.Tag, err)
		}
		fmt.Fprintf(output, "OK job=%s profile=%s task_type=%s task_id=%s created=%t status=%s\n",
			job.Tag, stored.ProfileID, stored.Type, stored.ID, created, stored.Status)
	}
	return nil
}

type jobCommand struct {
	taskType  core.TaskType
	profileID core.ProfileID
	payload   json.RawMessage
}

func commandsForJob(cfg appconfig.Config, job appconfig.Job) ([]jobCommand, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	profileCommand := func(taskType core.TaskType, build func(appconfig.Profile) (json.RawMessage, error)) ([]jobCommand, error) {
		targets := job.Action.TargetProfiles()
		if len(targets) == 0 {
			return nil, fmt.Errorf("job %q action requires profile or profiles", job.Tag)
		}
		commands := make([]jobCommand, 0, len(targets))
		for _, target := range targets {
			profile, exists := profiles[target]
			if !exists {
				return nil, fmt.Errorf("job %q references an unknown profile", job.Tag)
			}
			payload, err := build(profile)
			if err != nil {
				return nil, err
			}
			commands = append(commands, jobCommand{taskType: taskType, profileID: core.ProfileID(profile.Tag), payload: payload})
		}
		return commands, nil
	}
	switch job.Action.Type {
	case appconfig.JobActionResumeTouch, appconfig.JobActionProfileActivityObserve:
		taskType := core.TaskResumeTouch
		if job.Action.Type == appconfig.JobActionProfileActivityObserve {
			taskType = core.TaskProfileActivityObserve
		}
		return profileCommand(taskType, func(profile appconfig.Profile) (json.RawMessage, error) {
			resumeID := job.Action.Resume
			if resumeID == "" {
				resumeID = profile.Resume
			}
			if job.Action.Type == appconfig.JobActionResumeTouch {
				return json.Marshal(core.ResumeTouchPayload{ProfileID: core.ProfileID(profile.Tag), ResumeID: resumeID})
			}
			return json.Marshal(core.ProfileActivityObservePayload{ProfileID: core.ProfileID(profile.Tag), ResumeID: resumeID})
		})
	case appconfig.JobActionProfileSessionRefresh:
		return profileCommand(core.TaskProfileSessionRefresh, func(profile appconfig.Profile) (json.RawMessage, error) {
			return json.Marshal(core.ProfileSessionRefreshPayload{ProfileID: core.ProfileID(profile.Tag)})
		})
	case appconfig.JobActionConversationSync:
		return profileCommand(core.TaskConversationDiscover, func(profile appconfig.Profile) (json.RawMessage, error) {
			return json.Marshal(core.ConversationDiscoverPayload{ProfileID: core.ProfileID(profile.Tag)})
		})
	case appconfig.JobActionApplicationStateSync:
		return profileCommand(core.TaskApplicationStateSync, func(profile appconfig.Profile) (json.RawMessage, error) {
			return json.Marshal(core.ApplicationStateSyncPayload{ProfileID: core.ProfileID(profile.Tag)})
		})
	case appconfig.JobActionApplicationRetention:
		if job.Action.Retention == nil {
			return nil, fmt.Errorf("job %q has no retention settings", job.Tag)
		}
		return profileCommand(core.TaskApplicationRetention, func(profile appconfig.Profile) (json.RawMessage, error) {
			return json.Marshal(job.Action.Retention.Payload(core.ProfileID(profile.Tag)))
		})
	case appconfig.JobActionApplicationCampaign:
		profileIDs := make([]core.ProfileID, 0, len(job.Action.Profiles))
		for _, value := range job.Action.Profiles {
			profileIDs = append(profileIDs, core.ProfileID(value))
		}
		if len(profileIDs) == 0 {
			return nil, fmt.Errorf("job %q has no campaign profiles", job.Tag)
		}
		routes := make([]core.SearchID, 0, len(job.Action.Routes))
		for _, value := range job.Action.Routes {
			routes = append(routes, core.SearchID(value))
		}
		payload, err := json.Marshal(core.NewApplicationCampaignStartPayload(
			job.Tag, profileIDs, routes, job.Action.TargetSuccessful, job.Action.MaxInFlight,
		))
		if err != nil {
			return nil, err
		}
		return []jobCommand{{taskType: core.TaskApplicationCampaign, profileID: profileIDs[0], payload: payload}}, nil
	case appconfig.JobActionConversationFollowUpSelect:
		if job.Action.FollowUp == nil {
			return nil, fmt.Errorf("job %q has no follow-up selection settings", job.Tag)
		}
		return profileCommand(core.TaskConversationFollowUpSelect, func(profile appconfig.Profile) (json.RawMessage, error) {
			return json.Marshal(job.Action.FollowUp.Payload(core.ProfileID(profile.Tag)))
		})
	case appconfig.JobActionProfileStateReconcile:
		resources, err := cfg.BuildProfileStateResources()
		if err != nil {
			return nil, err
		}
		for _, resource := range resources {
			if resource.Tag == job.Action.Resource {
				payload, err := json.Marshal(core.ProfileStateReconcilePayload{ResourceTag: resource.Tag})
				if err != nil {
					return nil, err
				}
				return []jobCommand{{taskType: core.TaskProfileStateReconcile, profileID: resource.ProfileID, payload: payload}}, nil
			}
		}
		return nil, fmt.Errorf("job %q references an unknown profile state resource", job.Tag)
	default:
		return nil, fmt.Errorf("job %q has unsupported action %q", job.Tag, job.Action.Type)
	}
}

func adapterForCommand(cfg appconfig.Config, job appconfig.Job, command jobCommand) (string, error) {
	if job.Action.Type == appconfig.JobActionApplicationCampaign || job.Action.Type == appconfig.JobActionProfileStateReconcile {
		return adapterForJob(cfg, job)
	}
	profile, ok := configuredProfile(cfg, string(command.profileID))
	if !ok {
		return "", fmt.Errorf("job %q references an unknown profile", job.Tag)
	}
	return profile.Adapter, nil
}

func adapterForJob(cfg appconfig.Config, job appconfig.Job) (string, error) {
	switch job.Action.Type {
	case appconfig.JobActionResumeTouch, appconfig.JobActionProfileActivityObserve, appconfig.JobActionProfileSessionRefresh, appconfig.JobActionConversationSync, appconfig.JobActionConversationFollowUpSelect:
		profile, ok := configuredProfile(cfg, job.Action.Profile)
		if !ok {
			return "", fmt.Errorf("job %q references an unknown profile", job.Tag)
		}
		return profile.Adapter, nil
	case appconfig.JobActionApplicationCampaign:
		for _, search := range cfg.Searches {
			if search.Tag == job.Action.Routes[0] {
				return search.Adapter, nil
			}
		}
		return "", fmt.Errorf("job %q references an unknown route", job.Tag)
	case appconfig.JobActionProfileStateReconcile:
		resources, err := cfg.BuildProfileStateResources()
		if err != nil {
			return "", err
		}
		for _, resource := range resources {
			if resource.Tag != job.Action.Resource {
				continue
			}
			profile, exists := configuredProfile(cfg, string(resource.ProfileID))
			if !exists {
				return "", fmt.Errorf("job %q references an unknown profile", job.Tag)
			}
			return profile.Adapter, nil
		}
		return "", fmt.Errorf("job %q references an unknown profile state resource", job.Tag)
	default:
		return "", fmt.Errorf("job %q has unsupported action %q", job.Tag, job.Action.Type)
	}
}

func configuredProfile(cfg appconfig.Config, tag string) (appconfig.Profile, bool) {
	for _, profile := range cfg.Profiles {
		if profile.Tag == tag {
			return profile, true
		}
	}
	return appconfig.Profile{}, false
}

// Run executes the tool with the given arguments and returns a process exit
// code. It preserves the historical CLI contract of cmd/job-agent-trigger.
func Run(args []string) int {
	os.Args = append([]string{os.Args[0]}, args...)
	runMain()
	return 0
}
