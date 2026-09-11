package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestStoreKeepsMonotonicTestAttemptAcrossReopen(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	definition, err := core.NewProgressiveTestDefinition("hh", "vacancy:42", "Go basics", nil, now)
	if err != nil {
		t.Fatalf("new definition: %v", err)
	}
	if _, err := store.UpsertTestDefinition(ctx, definition); err != nil {
		t.Fatalf("store definition: %v", err)
	}
	base := core.TestAttempt{
		Platform: "hh", ProfileID: "primary", ExternalID: "42",
		TestDefinitionID: definition.ID, Status: core.TestAttemptFailed,
		Attempts: 1, ObservedAt: now, UpdatedAt: now,
	}
	if err := store.SaveTestAttempt(ctx, base); err != nil {
		t.Fatalf("save failed attempt: %v", err)
	}
	passed := base
	passed.Status = core.TestAttemptPassed
	passed.Attempts = 2
	passed.ObservedAt = now.Add(time.Minute)
	passed.UpdatedAt = now.Add(time.Minute)
	if err := store.SaveTestAttempt(ctx, passed); err != nil {
		t.Fatalf("save passed attempt: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()
	stored, found, err := store.LatestTestAttempt(ctx, "hh", "primary", "42")
	if err != nil || !found {
		t.Fatalf("latest: found=%v err=%v", found, err)
	}
	if !stored.Passed() || stored.Attempts != 2 || !stored.ObservedAt.Equal(passed.ObservedAt) {
		t.Fatalf("stored = %#v", stored)
	}
	if _, found, err := store.LatestTestAttempt(ctx, "hh", "primary", "missing"); err != nil || found {
		t.Fatalf("missing attempt: found=%v err=%v", found, err)
	}
}
