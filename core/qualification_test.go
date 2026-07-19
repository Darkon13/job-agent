package core

import (
	"testing"
	"time"
)

func TestPreferQualificationResultOnlyPromotesBetterVerifiedResult(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	score := func(value float64) *float64 { return &value }
	current := QualificationResult{
		Status: QualificationPassed, Score: score(8), MaxScore: score(10), Verified: true,
		AnswerBlockTag: "go-medium-v1", CompletedAt: now,
	}

	worse := QualificationResult{
		Status: QualificationFailed, Score: score(9), MaxScore: score(10), Verified: true,
		AnswerBlockTag: "go-medium-failed", CompletedAt: now.Add(time.Hour),
	}
	got, promoted, err := PreferQualificationResult(&current, worse)
	if err != nil {
		t.Fatalf("prefer worse: %v", err)
	}
	if promoted || got.AnswerBlockTag != current.AnswerBlockTag {
		t.Fatalf("failed result replaced passed result: %#v", got)
	}

	unverified := QualificationResult{
		Status: QualificationPassed, Score: score(10), MaxScore: score(10), Verified: false,
		AnswerBlockTag: "go-medium-unverified", CompletedAt: now.Add(2 * time.Hour),
	}
	got, promoted, err = PreferQualificationResult(&current, unverified)
	if err != nil {
		t.Fatalf("prefer unverified: %v", err)
	}
	if promoted || got.AnswerBlockTag != current.AnswerBlockTag {
		t.Fatalf("unverified result replaced verified result: %#v", got)
	}

	better := QualificationResult{
		Status: QualificationPassed, Score: score(9), MaxScore: score(10), Verified: true,
		AnswerBlockTag: "go-medium-v2", CompletedAt: now.Add(3 * time.Hour),
	}
	got, promoted, err = PreferQualificationResult(&current, better)
	if err != nil {
		t.Fatalf("prefer better: %v", err)
	}
	if !promoted || got.AnswerBlockTag != better.AnswerBlockTag {
		t.Fatalf("better result was not promoted: %#v", got)
	}
}

func TestQualificationResultIsReusableOnlyWhenVerifiedAndPassed(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	result := QualificationResult{
		Status: QualificationPassed, Verified: true, AnswerBlockTag: "go-basic", CompletedAt: now,
	}
	if !result.Reusable() {
		t.Fatal("expected verified passed result to be reusable")
	}
	result.Status = QualificationFailed
	if result.Reusable() {
		t.Fatal("failed result must not be reusable")
	}
}

func TestQualificationLevelsAreAdapterDefined(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	order := 2
	descriptor := QualificationDescriptor{
		FamilyID: "go", FamilyName: "Go", LevelID: "platform-expert", LevelName: "Экспертный", LevelOrder: &order,
	}
	definition, err := BuildQualificationTestDefinition("hh", Questionnaire{Questions: []Question{
		{ID: "q1", Text: "Choose", Kind: QuestionSingle, Options: []QuestionOption{{ID: "a", Text: "A"}}},
	}}, &descriptor, now)
	if err != nil {
		t.Fatalf("build qualification definition: %v", err)
	}
	if definition.Qualification == nil || definition.Qualification.LevelID != "platform-expert" {
		t.Fatalf("qualification level was not retained: %#v", definition.Qualification)
	}
	basic := descriptor
	basic.LevelID = "platform-basic"
	basic.LevelName = "Базовый"
	basicDefinition, err := BuildQualificationTestDefinition("hh", Questionnaire{Questions: []Question{
		{ID: "q1", Text: "Choose", Kind: QuestionSingle, Options: []QuestionOption{{ID: "a", Text: "A"}}},
	}}, &basic, now)
	if err != nil {
		t.Fatalf("build basic definition: %v", err)
	}
	if basicDefinition.LastAttemptFingerprint != definition.LastAttemptFingerprint || basicDefinition.ID == definition.ID {
		t.Fatalf("levels need shared content identity but distinct catalog IDs: %#v %#v", basicDefinition, definition)
	}
}
