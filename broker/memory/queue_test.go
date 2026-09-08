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

func TestQueueReadsTaskByIDWithoutExposingMutableState(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 14, 0, 0, 0, time.UTC)
	queue := NewQueue()
	task := testTask(t, "task-by-id", "key-by-id", now, nil)
	if _, err := queue.Enqueue(ctx, task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	stored, err := queue.TaskByID(ctx, task.ID)
	if err != nil || stored.ID != task.ID {
		t.Fatalf("task by id: %#v err=%v", stored, err)
	}
	stored.Payload[0] = '['
	again, err := queue.TaskByID(ctx, task.ID)
	if err != nil || string(again.Payload) != `{}` {
		t.Fatalf("stored task was mutated: %#v err=%v", again, err)
	}
	if _, err := queue.TaskByID(ctx, "missing"); !errors.Is(err, broker.ErrTaskNotFound) {
		t.Fatalf("missing task error = %v", err)
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

func TestQueueClaimsHigherPriorityBeforeOlderTask(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	queue := NewQueue()
	low := testTask(t, "task-low", "key-low", now, nil)
	low.Priority = -100
	high := testTask(t, "task-high", "key-high", now.Add(time.Second), nil)
	high.Priority = 100
	if _, err := queue.Enqueue(ctx, low); err != nil {
		t.Fatalf("enqueue low priority: %v", err)
	}
	if _, err := queue.Enqueue(ctx, high); err != nil {
		t.Fatalf("enqueue high priority: %v", err)
	}
	lease, found, err := queue.Claim(ctx, broker.ClaimParams{
		WorkerID: "worker", TaskType: core.TaskApplicationSubmit,
		Now: now.Add(time.Second), LeaseDuration: time.Minute,
	})
	if err != nil || !found || lease.Task.ID != high.ID || lease.Task.Priority != high.Priority {
		t.Fatalf("priority claim: found=%t lease=%#v err=%v", found, lease, err)
	}
}

func TestQueueBlocksApplicationWhileProfileStateApplyIsActive(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	queue := NewQueue()

	apply := testTask(t, "profile-apply", "profile-apply", now, nil)
	apply.Type = core.TaskProfileStateApply
	apply.ProfileID = "primary"
	sameProfile := testTask(t, "application-primary", "application-primary", now.Add(time.Second), nil)
	sameProfile.ProfileID = "primary"
	otherProfile := testTask(t, "application-secondary", "application-secondary", now.Add(2*time.Second), nil)
	otherProfile.ProfileID = "secondary"
	for _, task := range []core.Task{apply, sameProfile, otherProfile} {
		if _, err := queue.Enqueue(ctx, task); err != nil {
			t.Fatalf("enqueue %s: %v", task.ID, err)
		}
	}

	claim := broker.ClaimParams{
		WorkerID: "application-worker", TaskType: core.TaskApplicationSubmit,
		BlockedByTaskType: core.TaskProfileStateApply,
		Now:               now.Add(2 * time.Second), LeaseDuration: time.Minute,
	}
	lease, found, err := queue.Claim(ctx, claim)
	if err != nil || !found || lease.Task.ID != otherProfile.ID {
		t.Fatalf("other profile claim: found=%t lease=%#v err=%v", found, lease, err)
	}
	if err := queue.Complete(ctx, lease, claim.Now.Add(time.Millisecond)); err != nil {
		t.Fatalf("complete other profile: %v", err)
	}
	claim.Now = claim.Now.Add(time.Second)
	if _, found, err := queue.Claim(ctx, claim); err != nil || found {
		t.Fatalf("blocked profile was claimed: found=%t err=%v", found, err)
	}

	applyLease, found, err := queue.Claim(ctx, broker.ClaimParams{
		WorkerID: "profile-worker", TaskType: core.TaskProfileStateApply,
		Now: claim.Now, LeaseDuration: time.Minute,
	})
	if err != nil || !found || applyLease.Task.ID != apply.ID {
		t.Fatalf("profile apply claim: found=%t lease=%#v err=%v", found, applyLease, err)
	}
	if err := queue.Complete(ctx, applyLease, claim.Now.Add(time.Millisecond)); err != nil {
		t.Fatalf("complete profile apply: %v", err)
	}
	claim.Now = claim.Now.Add(2 * time.Millisecond)
	lease, found, err = queue.Claim(ctx, claim)
	if err != nil || !found || lease.Task.ID != sameProfile.ID || lease.Task.Attempts != 1 {
		t.Fatalf("unblocked application claim: found=%t lease=%#v err=%v", found, lease, err)
	}
}

func TestQueueFailedProfileStateApplyBlocksUntilDismissed(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC)
	queue := NewQueue()
	apply := testTask(t, "profile-apply-failed", "profile-apply-failed", now, nil)
	apply.Type = core.TaskProfileStateApply
	apply.ProfileID = "primary"
	application := testTask(t, "application-blocked", "application-blocked", now, nil)
	application.ProfileID = "primary"
	for _, task := range []core.Task{apply, application} {
		if _, err := queue.Enqueue(ctx, task); err != nil {
			t.Fatalf("enqueue %s: %v", task.ID, err)
		}
	}
	lease, found, err := queue.Claim(ctx, broker.ClaimParams{
		WorkerID: "profile-worker", TaskType: core.TaskProfileStateApply,
		Now: now, LeaseDuration: time.Minute,
	})
	if err != nil || !found {
		t.Fatalf("claim apply: found=%t err=%v", found, err)
	}
	if err := queue.Fail(ctx, lease, &core.OperationError{
		Category: core.ErrorPermanentFailure, Operation: "profile_state.apply", Message: "invalid field",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("fail apply: %v", err)
	}
	claim := broker.ClaimParams{
		WorkerID: "application-worker", TaskType: core.TaskApplicationSubmit,
		BlockedByTaskType: core.TaskProfileStateApply,
		Now:               now.Add(2 * time.Second), LeaseDuration: time.Minute,
	}
	if _, found, err := queue.Claim(ctx, claim); err != nil || found {
		t.Fatalf("application passed failed apply: found=%t err=%v", found, err)
	}
	dismissed, err := queue.DismissFailedTask(ctx, apply.IdempotencyKey, now.Add(3*time.Second))
	if err != nil || dismissed.Status != core.TaskDismissed || dismissed.Failure == nil {
		t.Fatalf("dismiss apply: task=%#v err=%v", dismissed, err)
	}
	claim.Now = now.Add(4 * time.Second)
	lease, found, err = queue.Claim(ctx, claim)
	if err != nil || !found || lease.Task.ID != application.ID || lease.Task.Attempts != 1 {
		t.Fatalf("claim after dismissal: found=%t lease=%#v err=%v", found, lease, err)
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
