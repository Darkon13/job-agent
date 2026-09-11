package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestStoreUpsertsQualificationOfferingsWithBestResult(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	order := 1
	offering := core.QualificationOffering{
		ID: "offer-1", Platform: "hh", ProfileID: "primary", ExternalID: "go-medium",
		Qualification: core.QualificationDescriptor{
			FamilyID: "go", FamilyName: "Go", LevelID: "medium", LevelName: "Средний", LevelOrder: &order,
		},
		Status: core.QualificationAvailable, ObservedAt: now,
	}
	if err := store.UpsertQualificationOfferings(ctx, "hh", "primary", []core.QualificationOffering{offering}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	score, maxScore := 9.0, 10.0
	attempt := core.QualificationAttempt{
		Platform: "hh", ProfileID: "primary", Qualification: offering.Qualification,
		Result: core.QualificationResult{
			Status: core.QualificationPassed, Score: &score, MaxScore: &maxScore,
			Verified: true, AnswerBlockTag: "hh-go-medium", CompletedAt: now,
		},
	}
	if _, promoted, err := store.SaveQualificationAttempt(ctx, attempt, now); err != nil || !promoted {
		t.Fatalf("save attempt: promoted=%v err=%v", promoted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	offerings, err := store.QualificationOfferings(ctx, "hh", "primary")
	if err != nil || len(offerings) != 1 {
		t.Fatalf("offerings = %#v err=%v", offerings, err)
	}
	if offerings[0].BestResult == nil || !offerings[0].BestResult.Reusable() {
		t.Fatalf("best result = %#v", offerings[0].BestResult)
	}
	offering.Status = core.QualificationInProgress
	offering.ObservedAt = now.Add(time.Minute)
	if err := store.UpsertQualificationOfferings(ctx, "hh", "primary", []core.QualificationOffering{offering}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	offerings, err = store.QualificationOfferings(ctx, "hh", "primary")
	if err != nil || offerings[0].Status != core.QualificationInProgress {
		t.Fatalf("status = %#v err=%v", offerings[0].Status, err)
	}
}
