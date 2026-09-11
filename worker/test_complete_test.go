package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

func testCompleteTask(t *testing.T, payload core.TestCompletePayload) core.Task {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return core.Task{Type: core.TaskTestComplete, ProfileID: payload.ProfileID, Payload: data, IdempotencyKey: "test.complete:test"}
}

func TestTestCompleteRecordsMonotonicAttempt(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	handler, err := NewTestCompleteHandler(repository, fixedClock{now: now})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	payload := core.TestCompletePayload{
		ProfileID: "primary", Platform: "hh", VacancyExternalID: "42", Status: core.TestAttemptFailed,
	}
	if err := handler.Handle(context.Background(), testCompleteTask(t, payload)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := handler.Handle(context.Background(), testCompleteTask(t, payload)); err != nil {
		t.Fatalf("replay: %v", err)
	}
	attempt, found, err := repository.LatestTestAttempt(context.Background(), "hh", "primary", "42")
	if err != nil || !found {
		t.Fatalf("latest: found=%v err=%v", found, err)
	}
	if attempt.Attempts != 1 || attempt.Status != core.TestAttemptFailed {
		t.Fatalf("attempt = %#v", attempt)
	}
	if attempt.TestDefinitionID != core.VacancyTestDefinitionID("hh", "42") {
		t.Fatalf("definition = %s", attempt.TestDefinitionID)
	}

	payload.Status = core.TestAttemptPassed
	if err := handler.Handle(context.Background(), testCompleteTask(t, payload)); err != nil {
		t.Fatalf("passed handle: %v", err)
	}
	attempt, _, err = repository.LatestTestAttempt(context.Background(), "hh", "primary", "42")
	if err != nil || !attempt.Passed() || attempt.Attempts != 2 {
		t.Fatalf("attempt = %#v err=%v", attempt, err)
	}

	payload.Status = core.TestAttemptFailed
	if err := handler.Handle(context.Background(), testCompleteTask(t, payload)); err != nil {
		t.Fatalf("failed handle: %v", err)
	}
	attempt, _, err = repository.LatestTestAttempt(context.Background(), "hh", "primary", "42")
	if err != nil || !attempt.Passed() || attempt.Attempts != 3 {
		t.Fatalf("attempt after regression = %#v err=%v", attempt, err)
	}
}

func TestTestCompleteRejectsInvalidPayload(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	handler, err := NewTestCompleteHandler(repository, fixedClock{now: now})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	cases := []core.TestCompletePayload{
		{ProfileID: "primary", Platform: "hh", VacancyExternalID: "42"},
		{ProfileID: "primary", Platform: "hh", VacancyExternalID: "42", Status: "unknown"},
		{Platform: "hh", VacancyExternalID: "42", Status: core.TestAttemptPassed},
	}
	for _, payload := range cases {
		if err := handler.Handle(context.Background(), testCompleteTask(t, payload)); err == nil {
			t.Fatalf("payload %#v unexpectedly passed", payload)
		}
	}
}
