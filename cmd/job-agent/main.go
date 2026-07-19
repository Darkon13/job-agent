package main

import (
	"context"
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
	transports := taskworker.NewConversationTransportRegistry()
	for _, profile := range cfg.Profiles {
		transport, ok := instances[profile.Adapter].(adapter.ConversationTransport)
		if !ok {
			continue
		}
		if err := transports.Register(core.ProfileID(profile.Tag), transport); err != nil {
			log.Fatalf("register conversation transport for profile %q: %v", profile.Tag, err)
		}
	}
	conversationHandlers, err := taskworker.NewConversationHandlers(
		store, conversationWorkflow, transports, taskworker.StaticMessageResolver{}, taskworker.SystemClock{},
	)
	if err != nil {
		log.Fatalf("create conversation handlers: %v", err)
	}
	workers, err := conversationWorkers(store, conversationHandlers)
	if err != nil {
		log.Fatalf("create conversation workers: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, cfg, conversationAPI.Handler(), conversationWorkflow, workers); err != nil {
		log.Fatal(err)
	}
}

func serve(ctx context.Context, cfg appconfig.Config, handler http.Handler, conversationWorkflow *workflow.ConversationWorkflow, workers []*taskworker.Worker) error {
	if _, err := conversationWorkflow.ReconcileDueFollowUps(ctx, time.Now().UTC()); err != nil {
		return fmt.Errorf("initial follow-up reconcile: %w", err)
	}
	go reconcileFollowUps(ctx, cfg.Server.ReconcileInterval(), conversationWorkflow)

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
		return fmt.Errorf("conversation worker: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown conversation API: %w", err)
		}
		return nil
	}
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
		instance, err := taskworker.New(consumer, definition.handler, taskworker.SystemClock{}, taskworker.Config{
			ID: "conversation-" + string(definition.taskType), TaskType: definition.taskType,
			LeaseDuration: 2 * time.Minute, HeartbeatInterval: 30 * time.Second,
			PollInterval: time.Second, RetryBaseDelay: 5 * time.Second,
			BlockedRetryDelay: 5 * time.Minute, MaxAttempts: 5,
		})
		if err != nil {
			return nil, err
		}
		result = append(result, instance)
	}
	return result, nil
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
