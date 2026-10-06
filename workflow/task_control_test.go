package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/broker"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
)

func TestTaskControlWorkflowRetriesAndDismissesByPublicTaskID(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 16, 0, 0, 0, time.UTC)
	queue := brokermemory.NewQueue()
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-1", Type: core.TaskResumeTouch, IdempotencyKey: "internal-key",
		Source: "test", ProfileID: "primary", CorrelationID: "correlation-1", Payload: json.RawMessage(`{}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if _, err := queue.Enqueue(ctx, task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	fail := func(at time.Time) {
		lease, found, err := queue.Claim(ctx, broker.ClaimParams{
			WorkerID: "worker", TaskType: task.Type, Now: at, LeaseDuration: time.Minute,
		})
		if err != nil || !found {
			t.Fatalf("claim: found=%t err=%v", found, err)
		}
		if err := queue.Fail(ctx, lease, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "resume.touch", Message: "missing resume",
		}, at.Add(time.Second)); err != nil {
			t.Fatalf("fail: %v", err)
		}
	}
	fail(now)
	control, err := NewTaskControlWorkflow(queue, fixedClock{now.Add(2 * time.Second)})
	if err != nil {
		t.Fatalf("new control workflow: %v", err)
	}
	retried, err := control.Retry(ctx, task.ID)
	if err != nil || retried.Status != core.TaskNew || retried.Attempts != 0 || retried.Failure != nil {
		t.Fatalf("retry = %#v err=%v", retried, err)
	}
	fail(now.Add(3 * time.Second))
	control.clock = fixedClock{now.Add(5 * time.Second)}
	dismissed, err := control.Dismiss(ctx, task.ID)
	if err != nil || dismissed.Status != core.TaskDismissed || dismissed.Failure == nil {
		t.Fatalf("dismiss = %#v err=%v", dismissed, err)
	}
	if _, err := control.Retry(ctx, task.ID); !errors.Is(err, broker.ErrTaskNotFailed) {
		t.Fatalf("retry dismissed error = %v", err)
	}
}

func TestTaskControlWorkflowCancelsQueuedTasksOnly(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 27, 20, 0, 0, 0, time.UTC)
	queue := brokermemory.NewQueue()
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-cancel", Type: core.TaskApplicationSubmit, IdempotencyKey: "cancel-key",
		Source: "test", ProfileID: "primary", CorrelationID: "correlation-cancel", Payload: json.RawMessage(`{}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if _, err := queue.Enqueue(ctx, task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	control, err := NewTaskControlWorkflow(queue, fixedClock{now.Add(time.Second)})
	if err != nil {
		t.Fatalf("new control workflow: %v", err)
	}
	cancelled, err := control.Cancel(ctx, task.ID, "cancelled from the dashboard")
	if err != nil || cancelled.Status != core.TaskDismissed || cancelled.Failure == nil ||
		cancelled.Failure.Message != "cancelled from the dashboard" {
		t.Fatalf("cancel = %#v err=%v", cancelled, err)
	}
	// A settled task cannot be cancelled again.
	if _, err := control.Cancel(ctx, task.ID, ""); !errors.Is(err, broker.ErrTaskNotCancellable) {
		t.Fatalf("second cancel error = %v", err)
	}

	// A running task is rejected too.
	running, err := core.NewTask(core.NewTaskParams{
		ID: "task-running", Type: core.TaskResumeTouch, IdempotencyKey: "running-key",
		Source: "test", ProfileID: "primary", CorrelationID: "correlation-running", Payload: json.RawMessage(`{}`),
	}, now)
	if err != nil {
		t.Fatalf("new running task: %v", err)
	}
	if _, err := queue.Enqueue(ctx, running); err != nil {
		t.Fatalf("enqueue running: %v", err)
	}
	if _, found, err := queue.Claim(ctx, broker.ClaimParams{
		WorkerID: "worker", TaskType: running.Type, Now: now.Add(2 * time.Second), LeaseDuration: time.Minute,
	}); err != nil || !found {
		t.Fatalf("claim running: found=%t err=%v", found, err)
	}
	if _, err := control.Cancel(ctx, running.ID, ""); !errors.Is(err, broker.ErrTaskNotCancellable) {
		t.Fatalf("running cancel error = %v", err)
	}
}
