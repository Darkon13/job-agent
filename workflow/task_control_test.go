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
