package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

func TestQueueReclaimsExpiredLeaseAndRejectsStaleWorker(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	queue := NewQueue()
	task := testTask(t, "task-1", "key-1", now, nil)
	if created, err := queue.Enqueue(ctx, task); err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	first, found, err := queue.Claim(ctx, broker.ClaimParams{WorkerID: "worker-a", Now: now, LeaseDuration: time.Minute})
	if err != nil || !found || first.Task.Attempts != 1 {
		t.Fatalf("first claim: found=%v lease=%#v err=%v", found, first, err)
	}
	if _, found, err := queue.Claim(ctx, broker.ClaimParams{WorkerID: "worker-b", Now: now.Add(30 * time.Second), LeaseDuration: time.Minute}); err != nil || found {
		t.Fatalf("active lease was claimed: found=%v err=%v", found, err)
	}
	second, found, err := queue.Claim(ctx, broker.ClaimParams{WorkerID: "worker-b", Now: now.Add(61 * time.Second), LeaseDuration: time.Minute})
	if err != nil || !found || second.Task.Attempts != 2 || second.Token == first.Token {
		t.Fatalf("reclaim: found=%v lease=%#v err=%v", found, second, err)
	}
	if err := queue.Complete(ctx, first, now.Add(62*time.Second)); !errors.Is(err, broker.ErrLeaseLost) {
		t.Fatalf("stale worker error = %v", err)
	}
	if err := queue.Complete(ctx, second, now.Add(70*time.Second)); err != nil {
		t.Fatalf("complete reclaimed task: %v", err)
	}
	if tasks := queue.Tasks(); len(tasks) != 1 || tasks[0].Status != core.TaskCompleted {
		t.Fatalf("unexpected tasks: %#v", tasks)
	}
}

func TestQueueRetryExtendAndDeadlineSweep(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	queue := NewQueue()
	if _, err := queue.Enqueue(ctx, testTask(t, "task-retry", "key-retry", now, nil)); err != nil {
		t.Fatalf("enqueue retry task: %v", err)
	}
	lease, found, err := queue.Claim(ctx, broker.ClaimParams{WorkerID: "worker", Now: now, LeaseDuration: time.Minute})
	if err != nil || !found {
		t.Fatalf("claim retry task: found=%v err=%v", found, err)
	}
	extended, err := queue.Extend(ctx, lease, now.Add(30*time.Second), now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	retryAt := now.Add(5 * time.Minute)
	operationError := &core.OperationError{Category: core.ErrorTemporaryFailure, Operation: "applications.apply", Message: "timeout"}
	if err := queue.Retry(ctx, extended, operationError, retryAt, now.Add(time.Minute)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, found, err := queue.Claim(ctx, broker.ClaimParams{WorkerID: "worker", Now: retryAt.Add(-time.Second), LeaseDuration: time.Minute}); err != nil || found {
		t.Fatalf("retry claimed too early: found=%v err=%v", found, err)
	}
	lease, found, err = queue.Claim(ctx, broker.ClaimParams{WorkerID: "worker", Now: retryAt, LeaseDuration: time.Minute})
	if err != nil || !found || lease.Task.Attempts != 2 {
		t.Fatalf("claim retry: found=%v lease=%#v err=%v", found, lease, err)
	}
	if err := queue.Fail(ctx, lease, &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "applications.apply"}, retryAt.Add(time.Second)); err != nil {
		t.Fatalf("fail retry: %v", err)
	}

	deadline := now.Add(10 * time.Minute)
	if _, err := queue.Enqueue(ctx, testTask(t, "task-deadline", "key-deadline", now, &deadline)); err != nil {
		t.Fatalf("enqueue deadline task: %v", err)
	}
	if _, found, err := queue.Claim(ctx, broker.ClaimParams{WorkerID: "worker", Now: deadline, LeaseDuration: time.Minute}); err != nil || found {
		t.Fatalf("expired task was claimed: found=%v err=%v", found, err)
	}
	tasks := queue.Tasks()
	for _, task := range tasks {
		if task.ID == "task-deadline" && (task.Status != core.TaskFailed || task.Failure == nil || task.Failure.Message != "task deadline expired") {
			t.Fatalf("deadline task was not failed: %#v", task)
		}
	}
}

func testTask(t *testing.T, id core.TaskID, key string, now time.Time, deadline *time.Time) core.Task {
	t.Helper()
	task, err := core.NewTask(core.NewTaskParams{
		ID: id, Type: core.TaskApplicationSubmit, IdempotencyKey: key,
		Source: "test", CorrelationID: core.CorrelationID("correlation-" + key),
		Payload: json.RawMessage(`{}`), Deadline: deadline,
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	return task
}
