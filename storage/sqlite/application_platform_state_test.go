package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func TestApplicationPlatformStateRoundTripAndRemovalTombstone(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "job-agent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	application, err := core.NewApplication("application-1", core.ApplicationKey{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "vacancy-1"},
	}, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	vacancy := core.Vacancy{
		Platform: "hh", ExternalID: "vacancy-1", URL: "https://hh.ru/vacancy/vacancy-1",
		Title: "Backend developer", State: core.VacancyStateOpen, ObservedAt: now.Add(-time.Hour),
	}
	if _, err := store.UpsertVacancy(ctx, vacancy); err != nil {
		t.Fatalf("store vacancy: %v", err)
	}
	if _, _, err := store.CreateApplication(ctx, application); err != nil {
		t.Fatalf("create application: %v", err)
	}
	stored, _ := store.ApplicationByID(ctx, application.ID)
	for _, status := range []core.ApplicationStatus{core.ApplicationPreparing, core.ApplicationReady, core.ApplicationSubmitting, core.ApplicationSubmitted} {
		previous := stored.Status
		if err := stored.Transition(status, stored.UpdatedAt.Add(time.Second)); err != nil {
			t.Fatalf("transition %s: %v", status, err)
		}
		if err := store.SaveApplication(ctx, stored, previous); err != nil {
			t.Fatalf("save %s: %v", status, err)
		}
	}
	viewed := true
	platformUpdated := now.Add(-time.Minute)
	state := core.ApplicationPlatformState{
		ApplicationID: application.ID, ExternalNegotiationID: "negotiation-1", PlatformState: "response",
		Disposition: core.ApplicationDispositionPending, ViewedByOpponent: &viewed,
		PlatformUpdatedAt: &platformUpdated, ObservedAt: now,
	}
	if err := store.SaveApplicationPlatformState(ctx, state); err != nil {
		t.Fatalf("save platform state: %v", err)
	}
	loaded, err := store.ApplicationPlatformState(ctx, application.ID)
	if err != nil || loaded.ViewedByOpponent == nil || !*loaded.ViewedByOpponent || loaded.ExternalNegotiationID != "negotiation-1" {
		t.Fatalf("loaded state=%#v err=%v", loaded, err)
	}
	candidates, err := store.ListApplicationsForRetention(ctx, "primary")
	if err != nil || len(candidates) != 1 || candidates[0].ID != application.ID {
		t.Fatalf("retention candidates=%#v err=%v", candidates, err)
	}
	if _, removed, err := store.RemoveApplication(ctx, application.ID, core.ApplicationRemoval{Reason: core.ApplicationRemovalRetentionStale, StaleBefore: now, ObservedAt: now}, now.Add(time.Minute)); err != nil || !removed {
		t.Fatalf("remove application: removed=%v err=%v", removed, err)
	}
	if _, err := store.ApplicationPlatformState(ctx, application.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("platform state survived removal: %v", err)
	}
	newApplication, _ := core.NewApplication("application-2", application.Key, now.Add(2*time.Minute))
	if _, _, err := store.CreateApplication(ctx, newApplication); !errors.Is(err, storage.ErrApplicationRemoved) {
		t.Fatalf("removed application was rediscovered: %v", err)
	}
}

func TestApplicationPlatformStateSkipsMissingApplication(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "job-agent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	state := core.ApplicationPlatformState{
		ApplicationID: "application-gone", ExternalNegotiationID: "negotiation-1", PlatformState: "response",
		Disposition: core.ApplicationDispositionPending, ObservedAt: now,
	}
	if err := store.SaveApplicationPlatformState(ctx, state); !errors.Is(err, storage.ErrApplicationRemoved) {
		t.Fatalf("missing application error = %v", err)
	}
}
