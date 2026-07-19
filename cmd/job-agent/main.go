package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/adapters/hh"
	"github.com/Darkon13/job-agent/api/httpapi"
	"github.com/Darkon13/job-agent/broker"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	jobscheduler "github.com/Darkon13/job-agent/scheduler"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
	taskworker "github.com/Darkon13/job-agent/worker"
	"github.com/Darkon13/job-agent/workflow"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("usage: %s <config.json>", os.Args[0])
	}

	registry := adapter.NewRegistry()
	if err := registry.Register(hh.Name, hh.New); err != nil {
		log.Fatal(err)
	}

	cfg, err := appconfig.Load(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	store, err := storesqlite.Open(cfg.Database.Path)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("close database: %v", err)
		}
	}()

	instances := make(map[string]adapter.Adapter, len(cfg.Adapters))
	for _, item := range cfg.Adapters {
		instance, err := registry.Open(item.Type, item.Settings)
		if err != nil {
			log.Fatalf("open adapter %q: %v", item.Tag, err)
		}
		for _, search := range cfg.Searches {
			if search.Adapter == item.Tag {
				if err := instance.ValidateSearch(search.Query); err != nil {
					log.Fatalf("validate search %q: %v", search.Tag, err)
				}
			}
		}
		instances[item.Tag] = instance
	}

	conversationWorkflow, err := workflow.NewConversationWorkflow(store, store, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	if err != nil {
		log.Fatalf("create conversation workflow: %v", err)
	}
	conversationAPI, err := httpapi.NewConversationAPI(store, conversationWorkflow)
	if err != nil {
		log.Fatalf("create conversation API: %v", err)
	}
	conversationTransports := taskworker.NewConversationTransportRegistry()
	applicationTransports := taskworker.NewApplicationTransportRegistry()
	resumeTouchers := taskworker.NewResumeToucherRegistry()
	applicationPlans := make(taskworker.StaticApplicationPlans)
	for _, profile := range cfg.Profiles {
		instance := instances[profile.Adapter]
		if transport, ok := instance.(adapter.ConversationTransport); ok {
			if err := conversationTransports.Register(core.ProfileID(profile.Tag), transport); err != nil {
				log.Fatalf("register conversation transport for profile %q: %v", profile.Tag, err)
			}
		}
		if transport, ok := instance.(adapter.ApplicationTransport); ok {
			if err := applicationTransports.Register(core.ProfileID(profile.Tag), transport); err != nil {
				log.Fatalf("register application transport for profile %q: %v", profile.Tag, err)
			}
		}
		if toucher, ok := instance.(adapter.ResumeToucher); ok {
			if err := resumeTouchers.Register(core.ProfileID(profile.Tag), toucher); err != nil {
				log.Fatalf("register resume toucher for profile %q: %v", profile.Tag, err)
			}
		} else if instance.Name() == hh.Name && profile.StateFile != "" {
			if _, err := os.Stat(profile.StateFile); err == nil {
				toucher, err := hh.NewResumeTouchTransport(profile.StateFile, nil)
				if err != nil {
					log.Fatalf("create HH resume toucher for profile %q: %v", profile.Tag, err)
				}
				if err := resumeTouchers.Register(core.ProfileID(profile.Tag), toucher); err != nil {
					log.Fatalf("register HH resume toucher for profile %q: %v", profile.Tag, err)
				}
			}
		}
		if profile.Resume != "" {
			applicationPlans[core.ProfileID(profile.Tag)] = taskworker.ApplicationPlan{ResumeID: profile.Resume}
		}
	}
	conversationHandlers, err := taskworker.NewConversationHandlers(
		store, conversationWorkflow, conversationTransports, taskworker.StaticMessageResolver{}, taskworker.SystemClock{},
	)
	if err != nil {
		log.Fatalf("create conversation handlers: %v", err)
	}
	workers, err := conversationWorkers(store, conversationHandlers)
	if err != nil {
		log.Fatalf("create conversation workers: %v", err)
	}
	if applicationTransports.Count() > 0 {
		applicationHandler, err := taskworker.NewApplicationHandler(
			store, applicationTransports, applicationPlans, taskworker.SystemClock{},
		)
		if err != nil {
			log.Fatalf("create application handler: %v", err)
		}
		applicationWorker, err := newTaskWorker(store, core.TaskApplicationSubmit, applicationHandler.Handle)
		if err != nil {
			log.Fatalf("create application worker: %v", err)
		}
		workers = append(workers, applicationWorker)
	}
	if resumeTouchers.Count() > 0 {
		resumeHandler, err := taskworker.NewResumeTouchHandler(resumeTouchers)
		if err != nil {
			log.Fatalf("create resume touch handler: %v", err)
		}
		resumeWorker, err := newTaskWorker(store, core.TaskResumeTouch, resumeHandler.Handle)
		if err != nil {
			log.Fatalf("create resume touch worker: %v", err)
		}
		workers = append(workers, resumeWorker)
	}
	definitions, err := resumeTouchDefinitions(cfg, instances, resumeTouchers)
	if err != nil {
		log.Fatalf("build scheduled jobs: %v", err)
	}
	scheduler, err := jobscheduler.New(store, store, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	if err != nil {
		log.Fatalf("create scheduler: %v", err)
	}
	if err := scheduler.Sync(context.Background(), definitions); err != nil {
		log.Fatalf("sync scheduled jobs: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, cfg, conversationAPI.Handler(), conversationWorkflow, scheduler, workers); err != nil {
		log.Fatal(err)
	}
}

func serve(ctx context.Context, cfg appconfig.Config, handler http.Handler, conversationWorkflow *workflow.ConversationWorkflow, scheduler *jobscheduler.Scheduler, workers []*taskworker.Worker) error {
	if _, err := conversationWorkflow.ReconcileDueFollowUps(ctx, time.Now().UTC()); err != nil {
		return fmt.Errorf("initial follow-up reconcile: %w", err)
	}
	go reconcileFollowUps(ctx, cfg.Server.ReconcileInterval(), conversationWorkflow)
	if _, err := scheduler.ReconcileDue(ctx); err != nil {
		return fmt.Errorf("initial job reconcile: %w", err)
	}
	go reconcileJobs(ctx, cfg.Server.SchedulerInterval(), scheduler)

	server := &http.Server{
		Addr:              cfg.Server.ListenAddress(),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	result := make(chan error, 1)
	workerErrors := make(chan error, len(workers))
	for _, instance := range workers {
		go func(instance *taskworker.Worker) {
			workerErrors <- instance.Run(ctx)
		}(instance)
	}
	go func() {
		log.Printf("job-agent API listening on http://%s", server.Addr)
		result <- server.ListenAndServe()
	}()

	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve conversation API: %w", err)
	case err := <-workerErrors:
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("task worker: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown conversation API: %w", err)
		}
		return nil
	}
}

func resumeTouchDefinitions(cfg appconfig.Config, instances map[string]adapter.Adapter, touchers *taskworker.ResumeToucherRegistry) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionResumeTouch {
			continue
		}
		profile := profiles[job.Action.Profile]
		profileID := core.ProfileID(profile.Tag)
		if !profile.Enabled || !touchers.Has(profileID) {
			continue
		}
		resumeID := job.Action.Resume
		if resumeID == "" {
			resumeID = profile.Resume
		}
		payload, err := json.Marshal(core.ResumeTouchPayload{ProfileID: profileID, ResumeID: resumeID})
		if err != nil {
			return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
		}
		instance := instances[profile.Adapter]
		for index, trigger := range job.Triggers {
			minimum, maximum := trigger.Jitter.Durations()
			definitions = append(definitions, jobscheduler.Definition{
				JobTag: job.Tag, TriggerIndex: index, Expression: trigger.Expression, Timezone: trigger.Timezone,
				ActionType: core.TaskResumeTouch, Platform: core.Platform(instance.Name()), ProfileID: profileID,
				Payload: payload, JitterMin: minimum, JitterMax: maximum,
			})
		}
	}
	return definitions, nil
}

func conversationWorkers(consumer broker.TaskConsumer, handlers *taskworker.ConversationHandlers) ([]*taskworker.Worker, error) {
	definitions := []struct {
		taskType core.TaskType
		handler  taskworker.HandlerFunc
	}{
		{core.TaskConversationSend, handlers.Send},
		{core.TaskConversationFollowUp, handlers.FollowUp},
		{core.TaskConversationMarkRead, handlers.MarkRead},
		{core.TaskConversationSync, handlers.Sync},
	}
	result := make([]*taskworker.Worker, 0, len(definitions))
	for _, definition := range definitions {
		instance, err := newTaskWorker(consumer, definition.taskType, definition.handler)
		if err != nil {
			return nil, err
		}
		result = append(result, instance)
	}
	return result, nil
}

func newTaskWorker(consumer broker.TaskConsumer, taskType core.TaskType, handler taskworker.HandlerFunc) (*taskworker.Worker, error) {
	return taskworker.New(consumer, handler, taskworker.SystemClock{}, taskworker.Config{
		ID: "worker-" + string(taskType), TaskType: taskType,
		LeaseDuration: 2 * time.Minute, HeartbeatInterval: 30 * time.Second,
		PollInterval: time.Second, RetryBaseDelay: 5 * time.Second,
		BlockedRetryDelay: 5 * time.Minute, MaxAttempts: 5,
	})
}

func reconcileFollowUps(ctx context.Context, interval time.Duration, conversationWorkflow *workflow.ConversationWorkflow) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			result, err := conversationWorkflow.ReconcileDueFollowUps(ctx, now.UTC())
			if err != nil {
				log.Printf("reconcile follow-ups: %v", err)
				continue
			}
			if result.TasksCreated > 0 {
				log.Printf("reconciled %d due follow-ups, created %d tasks", result.Due, result.TasksCreated)
			}
		}
	}
}

func reconcileJobs(ctx context.Context, interval time.Duration, scheduler *jobscheduler.Scheduler) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := scheduler.ReconcileDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("reconcile scheduled jobs: %v", err)
			}
		}
	}
}
