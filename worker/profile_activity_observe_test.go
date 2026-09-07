package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type fakeProfileActivityObserver struct {
	observation adapter.ProfileActivityObservation
}

func (observer fakeProfileActivityObserver) ObserveProfileActivity(context.Context, core.ProfileID, string) (adapter.ProfileActivityObservation, error) {
	return observer.observation, nil
}

func TestProfileActivityObserveHandlerStoresIdempotentSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 7, 14, 0, 0, 0, time.UTC)
	shows, views, streak, required := 35, 1, 2, 10
	registry := NewProfileActivityObserverRegistry()
	if err := registry.Register("primary", fakeProfileActivityObserver{observation: adapter.ProfileActivityObservation{
		ScoreHidden: true, SearchShows: &shows, Views: &views, ResponseStreak: &streak, ResponsesRequired: &required, ObservedAt: now,
	}}); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	repository := storagememory.NewRepository()
	handler, err := NewProfileActivityObserveHandler(repository, registry)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	payload, _ := json.Marshal(core.ProfileActivityObservePayload{ProfileID: "primary", ResumeID: "resume-1"})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-1", Type: core.TaskProfileActivityObserve, IdempotencyKey: "observe-1", Source: "test",
		Platform: "hh", ProfileID: "primary", CorrelationID: "correlation-1", Payload: payload,
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle observation: %v", err)
	}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("repeat observation: %v", err)
	}
	items, err := repository.ListProfileActivitySnapshots(context.Background(), storage.ProfileActivitySnapshotFilter{ProfileID: "primary"})
	if err != nil || len(items) != 1 || !items[0].ScoreHidden || items[0].SearchShows == nil || *items[0].SearchShows != 35 {
		t.Fatalf("snapshots=%#v err=%v", items, err)
	}
}
