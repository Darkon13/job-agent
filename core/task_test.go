package core

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTaskRetryLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	task, err := NewTask(NewTaskParams{
		ID: "task-1", Type: TaskApplicationSubmit,
		IdempotencyKey: "profile-1:hh:42", Source: "application-workflow",
		Platform: "hh", ProfileID: "profile-1", CorrelationID: "correlation-1",
		Payload: json.RawMessage(`{"application_id":"application-1"}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if err := task.Transition(TaskProcessing, now); err != nil {
		t.Fatalf("start: %v", err)
	}
	retryAt := now.Add(5 * time.Minute)
	if err := task.ScheduleRetry(retryAt, &OperationError{
		Category: ErrorTemporaryFailure, Operation: "applications.apply", Message: "upstream timeout",
	}, now.Add(time.Minute)); err != nil {
		t.Fatalf("schedule retry: %v", err)
	}
	if err := task.Transition(TaskProcessing, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expected early retry to fail")
	}
	if err := task.Transition(TaskProcessing, retryAt); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := task.Transition(TaskCompleted, retryAt.Add(time.Minute)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if task.Attempts != 2 || task.Failure != nil {
		t.Fatalf("unexpected task: %#v", task)
	}
}

func TestTaskConfirmationLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	task, err := NewTask(NewTaskParams{
		ID: "task-2", Type: TaskChallengeRespond, IdempotencyKey: "challenge-1",
		Source: "hh-browser", Platform: "hh", ProfileID: "profile-1",
		CorrelationID: "correlation-2", Payload: json.RawMessage(`{"challenge_id":"challenge-1"}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if err := task.Transition(TaskProcessing, now); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := task.Transition(TaskWaitingConfirmation, now.Add(time.Second)); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := task.Transition(TaskProcessing, now.Add(time.Minute)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if task.Attempts != 2 {
		t.Fatalf("unexpected attempts: %d", task.Attempts)
	}
}

func TestTaskRejectsAvailabilityOutsideDeadline(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Minute)
	_, err := NewTask(NewTaskParams{
		ID: "task-3", Type: TaskApplicationSubmit, IdempotencyKey: "application-1",
		Source: "application-workflow", CorrelationID: "correlation-3",
		Payload: json.RawMessage(`{}`), AvailableAt: deadline, Deadline: &deadline,
	}, now)
	if err == nil {
		t.Fatal("expected task availability at deadline to fail")
	}
}

func TestTaskRejectsRetryOutsideDeadline(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(10 * time.Minute)
	task, err := NewTask(NewTaskParams{
		ID: "task-deadline", Type: TaskApplicationSubmit, IdempotencyKey: "application-deadline",
		Source: "test", CorrelationID: "correlation-deadline", Payload: json.RawMessage(`{}`), Deadline: &deadline,
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if err := task.Transition(TaskProcessing, now); err != nil {
		t.Fatalf("start task: %v", err)
	}
	operationError := &OperationError{Category: ErrorTemporaryFailure, Operation: "applications.apply"}
	if err := task.ScheduleRetry(deadline, operationError, now.Add(time.Minute)); err == nil {
		t.Fatal("expected retry at deadline to fail")
	}
}

func TestEventValidation(t *testing.T) {
	event := Event{
		ID: "event-1", Type: EventVacancyDiscovered, Source: "hh-search",
		Platform: "hh", ProfileID: "profile-1", AggregateID: "hh:42",
		CorrelationID: "correlation-3", OccurredAt: time.Now(), Payload: json.RawMessage(`{"vacancy_id":"42"}`),
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("validate event: %v", err)
	}
	event.Payload = json.RawMessage(`{`)
	if err := event.Validate(); err == nil {
		t.Fatal("expected invalid payload to fail")
	}
}
