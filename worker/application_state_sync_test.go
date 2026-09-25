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

// removingPlatformStateRepository simulates retention deleting an application
// between listing and saving its observed platform state.
type removingPlatformStateRepository struct {
	*storagememory.Repository
	removed core.ApplicationID
}

func (repo removingPlatformStateRepository) SaveApplicationPlatformState(ctx context.Context, state core.ApplicationPlatformState) error {
	if state.ApplicationID == repo.removed {
		return storage.ErrApplicationRemoved
	}
	return repo.Repository.SaveApplicationPlatformState(ctx, state)
}

func TestApplicationStateSyncStoresFreshStatesWithoutTouchingApplications(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	storeSubmittedApplication(t, repository, "application-invited", "vacancy-invited", now.Add(-2*time.Hour))
	storeSubmittedApplication(t, repository, "application-untouched", "vacancy-untouched", now.Add(-time.Hour))

	viewed := true
	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", retentionObserver{result: adapter.ApplicationStateObservationResult{
		ObservedAt: now,
		Applications: []adapter.ApplicationStateObservation{
			{ExternalNegotiationID: "n-invited", ExternalVacancyID: "vacancy-invited", PlatformState: "INVITATION", Disposition: core.ApplicationDispositionInvited, ViewedByOpponent: &viewed},
			{ExternalNegotiationID: "n-other", ExternalVacancyID: "vacancy-other", PlatformState: "RESPONSE", Disposition: core.ApplicationDispositionPending},
		},
	}}); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	clock := &conversationClock{now: now}
	handler, err := NewApplicationStateSyncHandler(repository, observers, clock)
	if err != nil {
		t.Fatalf("new state sync handler: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationStateSyncPayload{ProfileID: "primary"})
	task := core.Task{ID: "state-sync-1", Type: core.TaskApplicationStateSync, ProfileID: "primary", Platform: "hh", Payload: payload}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle state sync: %v", err)
	}
	state, err := repository.ApplicationPlatformState(context.Background(), "application-invited")
	if err != nil || state.Disposition != core.ApplicationDispositionInvited || state.PlatformState != "INVITATION" {
		t.Fatalf("stored state = %#v err=%v", state, err)
	}
	if _, err := repository.ApplicationPlatformState(context.Background(), "application-untouched"); err == nil {
		t.Fatal("unmatched application received a platform state")
	}
	application, err := repository.ApplicationByID(context.Background(), "application-invited")
	if err != nil || application.Status != core.ApplicationSubmitted {
		t.Fatalf("application changed: %#v err=%v", application, err)
	}
}

func TestApplicationStateSyncRejectsStaleObservation(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", retentionObserver{result: adapter.ApplicationStateObservationResult{
		ObservedAt: now.Add(-time.Hour),
	}}); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	handler, err := NewApplicationStateSyncHandler(repository, observers, &conversationClock{now: now})
	if err != nil {
		t.Fatalf("new state sync handler: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationStateSyncPayload{ProfileID: "primary"})
	task := core.Task{ID: "state-sync-2", Type: core.TaskApplicationStateSync, ProfileID: "primary", Platform: "hh", Payload: payload}
	if err := handler.Handle(context.Background(), task); err == nil {
		t.Fatal("expected a stale observation to fail")
	}
}

func TestApplicationStateSyncSkipsApplicationsRemovedDuringObservation(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	storeSubmittedApplication(t, repository, "application-removed", "vacancy-removed", now.Add(-2*time.Hour))
	storeSubmittedApplication(t, repository, "application-kept", "vacancy-kept", now.Add(-time.Hour))
	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", retentionObserver{result: adapter.ApplicationStateObservationResult{
		ObservedAt: now,
		Applications: []adapter.ApplicationStateObservation{
			{ExternalNegotiationID: "n-removed", ExternalVacancyID: "vacancy-removed", PlatformState: "RESPONSE", Disposition: core.ApplicationDispositionPending},
			{ExternalNegotiationID: "n-kept", ExternalVacancyID: "vacancy-kept", PlatformState: "INVITATION", Disposition: core.ApplicationDispositionInvited},
		},
	}}); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	handler, err := NewApplicationStateSyncHandler(
		removingPlatformStateRepository{Repository: repository, removed: "application-removed"},
		observers, &conversationClock{now: now},
	)
	if err != nil {
		t.Fatalf("new state sync handler: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationStateSyncPayload{ProfileID: "primary"})
	task := core.Task{ID: "state-sync-removed", Type: core.TaskApplicationStateSync, ProfileID: "primary", Platform: "hh", Payload: payload}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle state sync: %v", err)
	}
	state, err := repository.ApplicationPlatformState(context.Background(), "application-kept")
	if err != nil || state.Disposition != core.ApplicationDispositionInvited {
		t.Fatalf("kept state = %#v err=%v", state, err)
	}
}
