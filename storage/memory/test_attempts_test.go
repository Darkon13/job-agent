package memory

import (
	"context"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestRepositoryKeepsMonotonicTestAttempt(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := NewRepository()
	base := core.TestAttempt{
		Platform: "hh", ProfileID: "primary", ExternalID: "42",
		TestDefinitionID: core.VacancyTestDefinitionID("hh", "42"),
		Status:           core.TestAttemptFailed, Attempts: 1, ObservedAt: now, UpdatedAt: now,
	}
	if err := repository.SaveTestAttempt(ctx, base); err != nil {
		t.Fatalf("save failed attempt: %v", err)
	}
	passed := base
	passed.Status = core.TestAttemptPassed
	passed.Attempts = 2
	passed.ObservedAt = now.Add(time.Minute)
	passed.UpdatedAt = now.Add(time.Minute)
	if err := repository.SaveTestAttempt(ctx, passed); err != nil {
		t.Fatalf("save passed attempt: %v", err)
	}
	regressed := base
	regressed.Status = core.TestAttemptFailed
	regressed.Attempts = 3
	regressed.ObservedAt = now.Add(2 * time.Minute)
	regressed.UpdatedAt = now.Add(2 * time.Minute)
	if err := repository.SaveTestAttempt(ctx, regressed); err != nil {
		t.Fatalf("save regressed attempt: %v", err)
	}
	stored, found, err := repository.LatestTestAttempt(ctx, "hh", "primary", "42")
	if err != nil || !found {
		t.Fatalf("latest: found=%v err=%v", found, err)
	}
	if !stored.Passed() || stored.Attempts != 3 || !stored.ObservedAt.Equal(passed.ObservedAt) {
		t.Fatalf("stored = %#v", stored)
	}
	if _, found, err := repository.LatestTestAttempt(ctx, "hh", "primary", "missing"); err != nil || found {
		t.Fatalf("missing attempt: found=%v err=%v", found, err)
	}
}
