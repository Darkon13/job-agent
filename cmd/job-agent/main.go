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
	appconfig "github.com/Darkon13/job-agent/config"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
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
	}

	conversationWorkflow, err := workflow.NewConversationWorkflow(store, store, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	if err != nil {
		log.Fatalf("create conversation workflow: %v", err)
	}
	conversationAPI, err := httpapi.NewConversationAPI(store, conversationWorkflow)
	if err != nil {
		log.Fatalf("create conversation API: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, cfg, conversationAPI.Handler(), conversationWorkflow); err != nil {
		log.Fatal(err)
	}
}

func serve(ctx context.Context, cfg appconfig.Config, handler http.Handler, conversationWorkflow *workflow.ConversationWorkflow) error {
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
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown conversation API: %w", err)
		}
		return nil
	}
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
