package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestStorePersistsQualificationAttemptsAndBest(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	score, maxScore := 6.0, 10.0
	failed := core.QualificationAttempt{
		Platform: "hh", ProfileID: "primary",
		Qualification: core.QualificationDescriptor{FamilyID: "go", FamilyName: "Go", LevelID: "medium", LevelName: "Средний"},
		Result: core.QualificationResult{
			Status: core.QualificationFailed, Score: &score, MaxScore: &maxScore,
			Verified: true, AnswerBlockTag: "hh-go-medium", CompletedAt: now,
		},
		AttemptFingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	if _, promoted, err := store.SaveQualificationAttempt(ctx, failed, now); err != nil || !promoted {
		t.Fatalf("failed attempt: promoted=%v err=%v", promoted, err)
	}
	passedScore, passedMax := 9.0, 10.0
	passed := failed
	passed.Result.Status = core.QualificationPassed
	passed.Result.Score, passed.Result.MaxScore = &passedScore, &passedMax
	passed.Result.CompletedAt = now.Add(time.Minute)
	if _, promoted, err := store.SaveQualificationAttempt(ctx, passed, now.Add(time.Minute)); err != nil || !promoted {
		t.Fatalf("passed attempt: promoted=%v err=%v", promoted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	best, found, err := store.BestQualificationResult(ctx, "hh", "primary", "go", "medium")
	if err != nil || !found || best.Status != core.QualificationPassed || !best.Reusable() {
		t.Fatalf("best = %#v found=%v err=%v", best, found, err)
	}
	history, err := store.QualificationAttempts(ctx, "hh", "primary", "go", "medium")
	if err != nil || len(history) != 2 || history[0].Result.Status != core.QualificationFailed {
		t.Fatalf("history = %#v err=%v", history, err)
	}
	if _, found, err := store.BestQualificationResult(ctx, "hh", "primary", "go", "hard"); err != nil || found {
		t.Fatalf("unknown level: found=%v err=%v", found, err)
	}
}
