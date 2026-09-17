package core

import (
	"strings"
	"testing"
	"time"
)

func TestCompleteObservedAttemptDeduplicatesRepeatedQuestions(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	definition, err := NewProgressiveTestDefinition("hh", "vacancy-1", "Test", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	questions := []Question{
		{ID: "q1", Text: "First?", Kind: QuestionSingle, Options: []QuestionOption{{ID: "a", Text: "A"}, {ID: "b", Text: "B"}}},
		{ID: "q2", Text: "Second?", Kind: QuestionText},
	}
	fingerprints := make([]string, 0, len(questions))
	for _, question := range questions {
		fingerprint, _, err := definition.ObserveQuestion(question, now)
		if err != nil {
			t.Fatal(err)
		}
		fingerprints = append(fingerprints, fingerprint)
	}

	repeated := append(append([]string(nil), fingerprints...), fingerprints[0])
	attempt, err := definition.CompleteObservedAttempt(repeated, now)
	if err != nil {
		t.Fatalf("repeated question should not fail the attempt: %v", err)
	}
	unique, err := definition.CompleteObservedAttempt(fingerprints, now)
	if err != nil {
		t.Fatal(err)
	}
	if attempt != unique {
		t.Fatalf("repeated-question fingerprint = %s, want %s", attempt, unique)
	}
	if definition.ObservedAttempts != 2 {
		t.Fatalf("observed attempts = %d, want 2", definition.ObservedAttempts)
	}

	unknown := strings.Repeat("0", 64)
	if _, err := definition.CompleteObservedAttempt([]string{fingerprints[0], unknown}, now); err == nil {
		t.Fatal("unknown fingerprint must still fail")
	}
}
