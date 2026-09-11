package memory

import (
	"context"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func qualificationAttempt(status core.QualificationStatus, score, maxScore float64, verified bool, completedAt time.Time) core.QualificationAttempt {
	return core.QualificationAttempt{
		Platform: "hh", ProfileID: "primary",
		Qualification: core.QualificationDescriptor{
			FamilyID: "go", FamilyName: "Go", LevelID: "medium", LevelName: "Средний",
		},
		Result: core.QualificationResult{
			Status: status, Score: &score, MaxScore: &maxScore,
			Verified: verified, AnswerBlockTag: "hh-go-medium", CompletedAt: completedAt,
		},
	}
}

func TestRepositoryKeepsMonotonicQualificationBest(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := NewRepository()
	failed := qualificationAttempt(core.QualificationFailed, 5, 10, true, now)
	best, promoted, err := repository.SaveQualificationAttempt(ctx, failed, now)
	if err != nil || !promoted || best.Reusable() {
		t.Fatalf("failed attempt: best=%#v promoted=%v err=%v", best, promoted, err)
	}
	passed := qualificationAttempt(core.QualificationPassed, 8, 10, true, now.Add(time.Minute))
	best, promoted, err = repository.SaveQualificationAttempt(ctx, passed, now.Add(time.Minute))
	if err != nil || !promoted || !best.Reusable() {
		t.Fatalf("passed attempt: best=%#v promoted=%v err=%v", best, promoted, err)
	}
	better := qualificationAttempt(core.QualificationPassed, 10, 10, true, now.Add(2*time.Minute))
	if _, promoted, err := repository.SaveQualificationAttempt(ctx, better, now.Add(2*time.Minute)); err != nil || !promoted {
		t.Fatalf("better attempt: promoted=%v err=%v", promoted, err)
	}
	regressed := qualificationAttempt(core.QualificationFailed, 1, 10, true, now.Add(3*time.Minute))
	best, promoted, err = repository.SaveQualificationAttempt(ctx, regressed, now.Add(3*time.Minute))
	if err != nil || promoted || best.Status != core.QualificationPassed {
		t.Fatalf("regression: best=%#v promoted=%v err=%v", best, promoted, err)
	}
	unverified := qualificationAttempt(core.QualificationPassed, 10, 10, false, now.Add(4*time.Minute))
	if _, promoted, err := repository.SaveQualificationAttempt(ctx, unverified, now.Add(4*time.Minute)); err != nil || promoted {
		t.Fatalf("unverified attempt: promoted=%v err=%v", promoted, err)
	}
	history, err := repository.QualificationAttempts(ctx, "hh", "primary", "go", "medium")
	if err != nil || len(history) != 5 {
		t.Fatalf("history = %d err=%v", len(history), err)
	}
}
