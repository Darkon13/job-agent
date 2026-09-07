package main

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
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
	"github.com/Darkon13/job-agent/workflow"
)

func main() {
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

	taskType, profileID, payload, err := commandForJob(cfg, *job)
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
	adapterTag, err := adapterForJob(cfg, *job)
	if err != nil {
		return err
	}
	platform := platforms[adapterTag]
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(taskID), Type: taskType, IdempotencyKey: *idempotencyKey,
		Source: "manual:" + job.Tag, Platform: platform, ProfileID: profileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
	}, now)
	if err != nil {
		return err
	}
	store, err := storesqlite.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer store.Close()
	created, err := store.Enqueue(ctx, task)
	if err != nil {
		return fmt.Errorf("enqueue job %q: %w", job.Tag, err)
	}
	stored, err := store.TaskByIdempotencyKey(ctx, *idempotencyKey)
	if err != nil {
		return fmt.Errorf("load triggered job %q: %w", job.Tag, err)
	}
	fmt.Fprintf(output, "OK job=%s task_type=%s task_id=%s created=%t status=%s\n",
		job.Tag, stored.Type, stored.ID, created, stored.Status)
	return nil
}

func commandForJob(cfg appconfig.Config, job appconfig.Job) (core.TaskType, core.ProfileID, json.RawMessage, error) {
	switch job.Action.Type {
	case appconfig.JobActionResumeTouch, appconfig.JobActionProfileActivityObserve:
		profile, ok := configuredProfile(cfg, job.Action.Profile)
		if !ok {
			return "", "", nil, fmt.Errorf("job %q references an unknown profile", job.Tag)
		}
		resumeID := job.Action.Resume
		if resumeID == "" {
			resumeID = profile.Resume
		}
		if job.Action.Type == appconfig.JobActionResumeTouch {
			payload, err := json.Marshal(core.ResumeTouchPayload{ProfileID: core.ProfileID(profile.Tag), ResumeID: resumeID})
			return core.TaskResumeTouch, core.ProfileID(profile.Tag), payload, err
		}
		payload, err := json.Marshal(core.ProfileActivityObservePayload{ProfileID: core.ProfileID(profile.Tag), ResumeID: resumeID})
		return core.TaskProfileActivityObserve, core.ProfileID(profile.Tag), payload, err
	case appconfig.JobActionApplicationCampaign:
		profiles := make([]core.ProfileID, 0, len(job.Action.Profiles))
		for _, value := range job.Action.Profiles {
			profiles = append(profiles, core.ProfileID(value))
		}
		routes := make([]core.SearchID, 0, len(job.Action.Routes))
		for _, value := range job.Action.Routes {
			routes = append(routes, core.SearchID(value))
		}
		payload, err := json.Marshal(core.NewApplicationCampaignStartPayload(
			job.Tag, profiles, routes, job.Action.TargetSuccessful, job.Action.MaxInFlight,
		))
		return core.TaskApplicationCampaign, profiles[0], payload, err
	case appconfig.JobActionProfileStateReconcile:
		resources, err := cfg.BuildProfileStateResources()
		if err != nil {
			return "", "", nil, err
		}
		for _, resource := range resources {
			if resource.Tag == job.Action.Resource {
				payload, err := json.Marshal(core.ProfileStateReconcilePayload{ResourceTag: resource.Tag})
				return core.TaskProfileStateReconcile, resource.ProfileID, payload, err
			}
		}
		return "", "", nil, fmt.Errorf("job %q references an unknown profile state resource", job.Tag)
	default:
		return "", "", nil, fmt.Errorf("job %q has unsupported action %q", job.Tag, job.Action.Type)
	}
}

func adapterForJob(cfg appconfig.Config, job appconfig.Job) (string, error) {
	switch job.Action.Type {
	case appconfig.JobActionResumeTouch, appconfig.JobActionProfileActivityObserve:
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
