package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type fakeApplicationTransport struct {
	command adapter.ApplicationSubmitCommand
	err     error
}

func (transport *fakeApplicationTransport) SubmitApplication(_ context.Context, command adapter.ApplicationSubmitCommand) (adapter.ApplicationSubmitResult, error) {
	transport.command = command
	if transport.err != nil {
		return adapter.ApplicationSubmitResult{}, transport.err
	}
	return adapter.ApplicationSubmitResult{ExternalNegotiationID: "negotiation-1"}, nil
}

func applicationFixture(t *testing.T, plans StaticApplicationPlans, transport *fakeApplicationTransport) (*ApplicationHandler, *storagememory.Repository, core.Task, *conversationClock) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	key := core.ApplicationKey{ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"}}
	application, err := core.NewApplication("application-1", key, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, _, err := repository.CreateApplication(ctx, application); err != nil {
		t.Fatalf("create application: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationSubmitPayload{ApplicationID: application.ID, Key: key})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-1", Type: core.TaskApplicationSubmit, IdempotencyKey: "application-submit-1",
		Source: "test", Platform: "hh", ProfileID: "profile-1", CorrelationID: "correlation-1", Payload: payload,
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	transports := NewApplicationTransportRegistry()
	if err := transports.Register("profile-1", transport); err != nil {
		t.Fatalf("register transport: %v", err)
	}
	clock := &conversationClock{now: now}
	handler, err := NewApplicationHandler(repository, transports, plans, clock)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	return handler, repository, task, clock
}

func TestApplicationHandlerSubmitsAndPersistsNegotiation(t *testing.T) {
	transport := &fakeApplicationTransport{}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{
		"profile-1": {ResumeID: "resume-1", Message: "Добрый день"},
	}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle application: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationSubmitted || application.ExternalNegotiationID != "negotiation-1" || application.Attempts != 1 {
		t.Fatalf("stored application: %#v err=%v", application, err)
	}
	if transport.command.ResumeID != "resume-1" || transport.command.Message != "Добрый день" || transport.command.IdempotencyKey != task.IdempotencyKey {
		t.Fatalf("transport command: %#v", transport.command)
	}
}

func TestApplicationHandlerWaitsForMissingResume(t *testing.T) {
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{}, &fakeApplicationTransport{})
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle application: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationWaitingValidation || application.Attempts != 0 {
		t.Fatalf("stored application: %#v err=%v", application, err)
	}
}

func TestApplicationHandlerRestoresReadyStateForRetry(t *testing.T) {
	transport := &fakeApplicationTransport{err: &core.OperationError{
		Category: core.ErrorTemporaryFailure, Operation: "applications.submit", Message: "timeout",
	}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": {ResumeID: "resume-1"}}, transport)
	if err := handler.Handle(context.Background(), task); !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("handler error = %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationReady || application.Attempts != 1 {
		t.Fatalf("stored application: %#v err=%v", application, err)
	}
}
