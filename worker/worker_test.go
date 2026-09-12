package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/broker"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

func workerConfig(taskType core.TaskType) Config {
	return Config{
		ID: "worker-1", TaskType: taskType, LeaseDuration: time.Minute,
		HeartbeatInterval: 20 * time.Second, PollInterval: time.Second,
		RetryBaseDelay: 5 * time.Second, BlockedRetryDelay: 5 * time.Minute, MaxAttempts: 3,
	}
}

func enqueueWorkerTask(t *testing.T, queue *brokermemory.Queue, id string, taskType core.TaskType, now time.Time) {
	t.Helper()
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(id), Type: taskType, IdempotencyKey: "key-" + id,
		Source: "test", CorrelationID: "correlation-" + core.CorrelationID(id),
		Payload: json.RawMessage(`{}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if _, err := queue.Enqueue(context.Background(), task); err != nil {
		t.Fatalf("enqueue task: %v", err)
	}
}

func TestNormalizeErrorKeepsHandlerCause(t *testing.T) {
	normalized := normalizeError(errors.New("browser session is unauthorized"), core.TaskApplicationSubmit)
	if normalized.Category != core.ErrorPermanentFailure || normalized.Operation != string(core.TaskApplicationSubmit) {
		t.Fatalf("normalized=%#v", normalized)
	}
	if !strings.Contains(normalized.Message, "browser session is unauthorized") {
		t.Fatalf("message lost the cause: %q", normalized.Message)
	}
	long := normalizeError(errors.New(strings.Repeat("x", 1000)), core.TaskApplicationSubmit)
	if len(long.Message) > 320 {
		t.Fatalf("message is not bounded: %d", len(long.Message))
	}
}

func TestWorkerClaimsOnlyConfiguredTaskType(t *testing.T) {
	now := time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)
	queue := brokermemory.NewQueue()
	enqueueWorkerTask(t, queue, "application", core.TaskApplicationSubmit, now.Add(-time.Minute))
	enqueueWorkerTask(t, queue, "conversation", core.TaskConversationSend, now)
	var handled core.TaskID
	instance, err := New(queue, HandlerFunc(func(_ context.Context, task core.Task) error {
		handled = task.ID
		return nil
	}), fixedClock{now}, workerConfig(core.TaskConversationSend))
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	worked, err := instance.RunOnce(context.Background())
	if err != nil || !worked || handled != "conversation" {
		t.Fatalf("run once: worked=%t handled=%s err=%v", worked, handled, err)
	}
	tasks := queue.Tasks()
	if tasks[0].Status != core.TaskNew || tasks[1].Status != core.TaskCompleted {
		t.Fatalf("unexpected task states: %#v", tasks)
	}
}

func TestWorkerRetriesTypedTemporaryError(t *testing.T) {
	now := time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)
	queue := brokermemory.NewQueue()
	enqueueWorkerTask(t, queue, "temporary", core.TaskConversationSync, now)
	instance, err := New(queue, HandlerFunc(func(context.Context, core.Task) error {
		return &core.OperationError{Category: core.ErrorTemporaryFailure, Operation: "conversations.sync", Message: "timeout"}
	}), fixedClock{now}, workerConfig(core.TaskConversationSync))
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	if worked, err := instance.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("run once: worked=%t err=%v", worked, err)
	}
	task := queue.Tasks()[0]
	if task.Status != core.TaskRetryScheduled || !task.AvailableAt.Equal(now.Add(5*time.Second)) || task.Failure == nil || task.Failure.Category != core.ErrorTemporaryFailure {
		t.Fatalf("unexpected retry task: %#v", task)
	}
}

func TestWorkerSchedulesRateLimitAtRetryAfterAndReleasesLease(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	retryAt := now.Add(2 * time.Minute)
	queue := brokermemory.NewQueue()
	enqueueWorkerTask(t, queue, "search", core.TaskVacancySearchPage, now)
	instance, err := New(queue, HandlerFunc(func(context.Context, core.Task) error {
		return &core.OperationError{
			Category: core.ErrorRateLimited, Operation: "vacancies.search.global",
			Platform: "hh", RetryAfter: &retryAt,
		}
	}), fixedClock{now}, workerConfig(core.TaskVacancySearchPage))
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	if worked, err := instance.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("run once: worked=%t err=%v", worked, err)
	}
	task := queue.Tasks()[0]
	if task.Status != core.TaskRetryScheduled || !task.AvailableAt.Equal(retryAt) {
		t.Fatalf("unexpected rate-limited task: %#v", task)
	}
	if lease, found, err := queue.Claim(context.Background(), broker.ClaimParams{
		WorkerID: "other", TaskType: core.TaskVacancySearchPage, Now: now.Add(time.Second), LeaseDuration: time.Minute,
	}); err != nil || found || lease.Token != "" {
		t.Fatalf("rate-limited task retained an active lease: found=%t lease=%#v err=%v", found, lease, err)
	}
}

func TestWorkerSchedulesBlockedCategoryWithoutKeepingLease(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	queue := brokermemory.NewQueue()
	enqueueWorkerTask(t, queue, "auth", core.TaskVacancySearchPage, now)
	instance, err := New(queue, HandlerFunc(func(context.Context, core.Task) error {
		return &core.OperationError{Category: core.ErrorUnauthorized, Operation: "vacancies.search.global"}
	}), fixedClock{now}, workerConfig(core.TaskVacancySearchPage))
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}
	if worked, err := instance.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("run once: worked=%t err=%v", worked, err)
	}
	task := queue.Tasks()[0]
	if task.Status != core.TaskRetryScheduled || !task.AvailableAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("unexpected blocked retry: %#v", task)
	}
}

func TestWorkerFailsUntypedAndUnsupportedErrors(t *testing.T) {
	for name, handlerError := range map[string]error{
		"plain":       errors.New("bug"),
		"unsupported": &core.OperationError{Category: core.ErrorUnsupported, Operation: "conversation.send"},
		"conflict":    &core.OperationError{Category: core.ErrorConflict, Operation: "profile_state.apply"},
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)
			queue := brokermemory.NewQueue()
			enqueueWorkerTask(t, queue, name, core.TaskConversationSend, now)
			instance, err := New(queue, HandlerFunc(func(context.Context, core.Task) error { return handlerError }), fixedClock{now}, workerConfig(core.TaskConversationSend))
			if err != nil {
				t.Fatalf("new worker: %v", err)
			}
			if _, err := instance.RunOnce(context.Background()); err != nil {
				t.Fatalf("run once: %v", err)
			}
			if task := queue.Tasks()[0]; task.Status != core.TaskFailed {
				t.Fatalf("unexpected task: %#v", task)
			}
		})
	}
}
