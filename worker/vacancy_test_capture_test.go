package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type stubVacancyTestCapturer struct {
	questionnaire core.Questionnaire
	err           error
	calls         []core.VacancyKey
}

func (stub *stubVacancyTestCapturer) CaptureVacancyTest(_ context.Context, _ core.ProfileID, key core.VacancyKey) (core.Questionnaire, error) {
	stub.calls = append(stub.calls, key)
	return stub.questionnaire, stub.err
}

func testCaptureTask(t *testing.T, profileID core.ProfileID, platform core.Platform, externalID string) core.Task {
	t.Helper()
	payload, err := json.Marshal(core.TestCapturePayload{ProfileID: profileID, Platform: platform, VacancyExternalID: externalID})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return core.Task{
		ID: "test-capture-task", Type: core.TaskTestCapture, ProfileID: profileID, Payload: payload,
		CorrelationID: "correlation-1", IdempotencyKey: "test.capture:" + externalID,
	}
}

func TestVacancyTestCaptureStoresProgressiveCatalog(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	capturer := &stubVacancyTestCapturer{questionnaire: core.Questionnaire{
		Title: "Go basics",
		Questions: []core.Question{
			{ID: "1", Text: "Pick a language", Kind: core.QuestionSingle, Options: []core.QuestionOption{{ID: "10", Text: "Go"}, {ID: "11", Text: "Python"}}},
			{ID: "2", Text: "Explain experience", Kind: core.QuestionText},
		},
	}}
	capturers := NewVacancyTestCapturerRegistry()
	if err := capturers.Register("primary", capturer); err != nil {
		t.Fatalf("register: %v", err)
	}
	repository := storagememory.NewRepository()
	handler, err := NewVacancyTestCaptureHandler(capturers, repository, fixedClock{now: now})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if err := handler.Handle(context.Background(), testCaptureTask(t, "primary", "hh", "42")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	definitions, err := repository.ListTestDefinitions(context.Background(), storage.TestDefinitionFilter{Platform: "hh"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(definitions) != 1 || len(definitions[0].Questions) != 2 || definitions[0].ObservedAttempts != 1 {
		t.Fatalf("definitions = %#v", definitions)
	}
	if definitions[0].Title != "Go basics" || definitions[0].ExternalID != "vacancy:42" {
		t.Fatalf("definition = %#v", definitions[0])
	}

	capturer.questionnaire.Questions = append(capturer.questionnaire.Questions,
		core.Question{ID: "3", Text: "Rate yourself", Kind: core.QuestionMultiple, Options: []core.QuestionOption{{ID: "20", Text: "Junior"}, {ID: "21", Text: "Senior"}}},
	)
	if err := handler.Handle(context.Background(), testCaptureTask(t, "primary", "hh", "42")); err != nil {
		t.Fatalf("second handle: %v", err)
	}
	stored, err := repository.TestDefinition(context.Background(), definitions[0].ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(stored.Questions) != 3 || stored.ObservedAttempts != 2 {
		t.Fatalf("stored = %#v", stored)
	}
	for _, question := range stored.Questions {
		if question.FirstSeenAt.IsZero() || question.LastSeenAt.Before(question.FirstSeenAt) {
			t.Fatalf("question timestamps = %#v", question)
		}
	}
}

func TestVacancyTestCaptureRejectsUnknownProfile(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	capturers := NewVacancyTestCapturerRegistry()
	handler, err := NewVacancyTestCaptureHandler(capturers, storagememory.NewRepository(), fixedClock{now: now})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	err = handler.Handle(context.Background(), testCaptureTask(t, "primary", "hh", "42"))
	var operationErr *core.OperationError
	if !errors.As(err, &operationErr) || operationErr.Category != core.ErrorPermanentFailure {
		t.Fatalf("error = %v", err)
	}
}

func TestVacancyTestCapturePropagatesAdapterError(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	expected := &core.OperationError{Category: core.ErrorUnauthorized, Operation: "vacancies.test.capture", Message: "HH browser session requires authentication"}
	capturer := &stubVacancyTestCapturer{err: expected}
	capturers := NewVacancyTestCapturerRegistry()
	if err := capturers.Register("primary", capturer); err != nil {
		t.Fatalf("register: %v", err)
	}
	handler, err := NewVacancyTestCaptureHandler(capturers, storagememory.NewRepository(), fixedClock{now: now})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	err = handler.Handle(context.Background(), testCaptureTask(t, "primary", "hh", "42"))
	if !errors.Is(err, expected) {
		t.Fatalf("error = %v", err)
	}
	if len(capturer.calls) != 1 || capturer.calls[0].ExternalID != "42" {
		t.Fatalf("calls = %#v", capturer.calls)
	}
}

var _ adapter.VacancyTestCapturer = (*stubVacancyTestCapturer)(nil)
