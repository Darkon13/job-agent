package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

func TestTaskLeaseSurvivesReopenAndCanBeReclaimed(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	task := sqliteTask(t, "task-lease", "key-lease", now, nil)
	if created, err := store.Enqueue(ctx, task); err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	first, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "worker-a", Now: now, LeaseDuration: time.Minute})
	if err != nil || !found || first.Task.Attempts != 1 {
		t.Fatalf("first claim: found=%v lease=%#v err=%v", found, first, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "worker-b", Now: now.Add(30 * time.Second), LeaseDuration: time.Minute}); err != nil || found {
		t.Fatalf("active lease claimed after reopen: found=%v err=%v", found, err)
	}
	second, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "worker-b", Now: now.Add(61 * time.Second), LeaseDuration: time.Minute})
	if err != nil || !found || second.Task.Attempts != 2 || second.Token == first.Token {
		t.Fatalf("reclaim: found=%v lease=%#v err=%v", found, second, err)
	}
	if err := store.Complete(ctx, first, now.Add(62*time.Second)); !errors.Is(err, broker.ErrLeaseLost) {
		t.Fatalf("stale worker error = %v", err)
	}
	extended, err := store.Extend(ctx, second, now.Add(70*time.Second), now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("extend reclaimed lease: %v", err)
	}
	if err := store.Complete(ctx, extended, now.Add(130*time.Second)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	stored, err := store.TaskByIdempotencyKey(ctx, "key-lease")
	if err != nil || stored.Status != core.TaskCompleted || stored.Attempts != 2 {
		t.Fatalf("stored task: %#v err=%v", stored, err)
	}
}

func TestTaskClaimIsAtomicAcrossWorkers(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Enqueue(ctx, sqliteTask(t, "task-atomic", "key-atomic", now, nil)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	const workers = 8
	var wait sync.WaitGroup
	wait.Add(workers)
	results := make(chan bool, workers)
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		go func(worker int) {
			defer wait.Done()
			_, found, err := store.Claim(ctx, broker.ClaimParams{
				WorkerID: "worker-" + string(rune('a'+worker)), Now: now, LeaseDuration: time.Minute,
			})
			results <- found
			errorsChannel <- err
		}(index)
	}
	wait.Wait()
	close(results)
	close(errorsChannel)
	claimed := 0
	for found := range results {
		if found {
			claimed++
		}
	}
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent claim: %v", err)
		}
	}
	if claimed != 1 {
		t.Fatalf("claim count = %d, want 1", claimed)
	}
}

func TestTaskClaimFiltersByType(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	application := sqliteTask(t, "application", "application", now, nil)
	conversation := sqliteTask(t, "conversation", "conversation", now, nil)
	conversation.Type = core.TaskConversationSend
	if _, err := store.Enqueue(ctx, application); err != nil {
		t.Fatalf("enqueue application: %v", err)
	}
	if _, err := store.Enqueue(ctx, conversation); err != nil {
		t.Fatalf("enqueue conversation: %v", err)
	}
	lease, found, err := store.Claim(ctx, broker.ClaimParams{
		WorkerID: "conversation-worker", TaskType: core.TaskConversationSend,
		Now: now, LeaseDuration: time.Minute,
	})
	if err != nil || !found || lease.Task.ID != conversation.ID {
		t.Fatalf("filtered claim: found=%t lease=%#v err=%v", found, lease, err)
	}
}

func TestTaskClaimOrdersByPriorityThenFIFO(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	low := sqliteTask(t, "task-low", "key-low", now, nil)
	low.Priority = -100
	firstHigh := sqliteTask(t, "task-high-first", "key-high-first", now.Add(time.Second), nil)
	firstHigh.Priority = 100
	secondHigh := sqliteTask(t, "task-high-second", "key-high-second", now.Add(2*time.Second), nil)
	secondHigh.Priority = 100
	for _, task := range []core.Task{low, secondHigh, firstHigh} {
		if _, err := store.Enqueue(ctx, task); err != nil {
			t.Fatalf("enqueue %s: %v", task.ID, err)
		}
	}

	for index, expected := range []core.TaskID{firstHigh.ID, secondHigh.ID, low.ID} {
		claimAt := now.Add(3*time.Second + time.Duration(index)*time.Second)
		lease, found, err := store.Claim(ctx, broker.ClaimParams{
			WorkerID: "worker", TaskType: core.TaskApplicationSubmit,
			Now: claimAt, LeaseDuration: time.Minute,
		})
		if err != nil || !found || lease.Task.ID != expected {
			t.Fatalf("claim %d: expected=%s found=%t lease=%#v err=%v", index, expected, found, lease, err)
		}
		if err := store.Complete(ctx, lease, claimAt.Add(time.Millisecond)); err != nil {
			t.Fatalf("complete %s: %v", lease.Task.ID, err)
		}
	}
}

func TestTaskClaimBlocksApplicationWhileProfileStateApplyIsActive(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	apply := sqliteTask(t, "profile-apply", "profile-apply", now, nil)
	apply.Type = core.TaskProfileStateApply
	apply.ProfileID = "primary"
	sameProfile := sqliteTask(t, "application-primary", "application-primary", now.Add(time.Second), nil)
	sameProfile.ProfileID = "primary"
	otherProfile := sqliteTask(t, "application-secondary", "application-secondary", now.Add(2*time.Second), nil)
	otherProfile.ProfileID = "secondary"
	for _, task := range []core.Task{apply, sameProfile, otherProfile} {
		if _, err := store.Enqueue(ctx, task); err != nil {
			t.Fatalf("enqueue %s: %v", task.ID, err)
		}
	}

	claim := broker.ClaimParams{
		WorkerID: "application-worker", TaskType: core.TaskApplicationSubmit,
		BlockedByTaskType: core.TaskProfileStateApply,
		Now:               now.Add(2 * time.Second), LeaseDuration: time.Minute,
	}
	lease, found, err := store.Claim(ctx, claim)
	if err != nil || !found || lease.Task.ID != otherProfile.ID {
		t.Fatalf("other profile claim: found=%t lease=%#v err=%v", found, lease, err)
	}
	if err := store.Complete(ctx, lease, claim.Now.Add(time.Millisecond)); err != nil {
		t.Fatalf("complete other profile: %v", err)
	}
	claim.Now = claim.Now.Add(time.Second)
	if _, found, err := store.Claim(ctx, claim); err != nil || found {
		t.Fatalf("blocked profile was claimed: found=%t err=%v", found, err)
	}

	applyLease, found, err := store.Claim(ctx, broker.ClaimParams{
		WorkerID: "profile-worker", TaskType: core.TaskProfileStateApply,
		Now: claim.Now, LeaseDuration: time.Minute,
	})
	if err != nil || !found || applyLease.Task.ID != apply.ID {
		t.Fatalf("profile apply claim: found=%t lease=%#v err=%v", found, applyLease, err)
	}
	if err := store.Complete(ctx, applyLease, claim.Now.Add(time.Millisecond)); err != nil {
		t.Fatalf("complete profile apply: %v", err)
	}
	claim.Now = claim.Now.Add(2 * time.Millisecond)
	lease, found, err = store.Claim(ctx, claim)
	if err != nil || !found || lease.Task.ID != sameProfile.ID || lease.Task.Attempts != 1 {
		t.Fatalf("unblocked application claim: found=%t lease=%#v err=%v", found, lease, err)
	}
}

func TestTaskClaimFailedProfileStateApplyBlocksUntilRestarted(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	apply := sqliteTask(t, "profile-apply-failed", "profile-apply-failed", now, nil)
	apply.Type = core.TaskProfileStateApply
	apply.ProfileID = "primary"
	application := sqliteTask(t, "application-blocked", "application-blocked", now, nil)
	application.ProfileID = "primary"
	for _, task := range []core.Task{apply, application} {
		if _, err := store.Enqueue(ctx, task); err != nil {
			t.Fatalf("enqueue %s: %v", task.ID, err)
		}
	}
	lease, found, err := store.Claim(ctx, broker.ClaimParams{
		WorkerID: "profile-worker", TaskType: core.TaskProfileStateApply,
		Now: now, LeaseDuration: time.Minute,
	})
	if err != nil || !found {
		t.Fatalf("claim apply: found=%t err=%v", found, err)
	}
	if err := store.Fail(ctx, lease, &core.OperationError{
		Category: core.ErrorPermanentFailure, Operation: "profile_state.apply", Message: "invalid field",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("fail apply: %v", err)
	}
	applicationClaim := broker.ClaimParams{
		WorkerID: "application-worker", TaskType: core.TaskApplicationSubmit,
		BlockedByTaskType: core.TaskProfileStateApply,
		Now:               now.Add(2 * time.Second), LeaseDuration: time.Minute,
	}
	if _, found, err := store.Claim(ctx, applicationClaim); err != nil || found {
		t.Fatalf("application passed failed apply: found=%t err=%v", found, err)
	}
	restarted, err := store.RestartFailedTask(ctx, apply.IdempotencyKey, now.Add(3*time.Second))
	if err != nil || restarted.Status != core.TaskNew || restarted.Attempts != 0 || restarted.Failure != nil {
		t.Fatalf("restart apply: task=%#v err=%v", restarted, err)
	}
	applicationClaim.Now = now.Add(3 * time.Second)
	if _, found, err := store.Claim(ctx, applicationClaim); err != nil || found {
		t.Fatalf("application passed restarted apply: found=%t err=%v", found, err)
	}
	lease, found, err = store.Claim(ctx, broker.ClaimParams{
		WorkerID: "profile-worker", TaskType: core.TaskProfileStateApply,
		Now: now.Add(3 * time.Second), LeaseDuration: time.Minute,
	})
	if err != nil || !found {
		t.Fatalf("claim restarted apply: found=%t err=%v", found, err)
	}
	if err := store.Fail(ctx, lease, &core.OperationError{
		Category: core.ErrorPermanentFailure, Operation: "profile_state.apply", Message: "still invalid",
	}, now.Add(4*time.Second)); err != nil {
		t.Fatalf("fail restarted apply: %v", err)
	}
	dismissed, err := store.DismissFailedTask(ctx, apply.IdempotencyKey, now.Add(5*time.Second))
	if err != nil || dismissed.Status != core.TaskDismissed || dismissed.Failure == nil {
		t.Fatalf("dismiss apply: task=%#v err=%v", dismissed, err)
	}
	applicationClaim.Now = now.Add(6 * time.Second)
	lease, found, err = store.Claim(ctx, applicationClaim)
	if err != nil || !found || lease.Task.ID != application.ID || lease.Task.Attempts != 1 {
		t.Fatalf("claim after dismissal: found=%t lease=%#v err=%v", found, lease, err)
	}
	// An explicit operator retry revives even a dismissed task and makes it
	// block dependent applications again.
	requeued, err := store.RequeueTask(ctx, apply.IdempotencyKey, now.Add(7*time.Second))
	if err != nil || requeued.Status != core.TaskNew || requeued.Attempts != 0 || requeued.Failure != nil {
		t.Fatalf("requeue dismissed apply: task=%#v err=%v", requeued, err)
	}
	if _, err := store.RequeueTask(ctx, apply.IdempotencyKey, now.Add(8*time.Second)); !errors.Is(err, broker.ErrTaskNotRequeueable) {
		t.Fatalf("requeue queued apply: err=%v", err)
	}
	lease, found, err = store.Claim(ctx, broker.ClaimParams{
		WorkerID: "profile-worker", TaskType: core.TaskProfileStateApply,
		Now: now.Add(8 * time.Second), LeaseDuration: time.Minute,
	})
	if err != nil || !found || lease.Task.ID != apply.ID {
		t.Fatalf("claim requeued apply: found=%t lease=%#v err=%v", found, lease, err)
	}
}

func TestTaskRetryAndDeadlineSweepAreDurable(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Enqueue(ctx, sqliteTask(t, "task-retry", "key-retry", now, nil)); err != nil {
		t.Fatalf("enqueue retry: %v", err)
	}
	lease, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "worker", Now: now, LeaseDuration: time.Minute})
	if err != nil || !found {
		t.Fatalf("claim: found=%v err=%v", found, err)
	}
	retryAt := now.Add(5 * time.Minute)
	operationError := &core.OperationError{Category: core.ErrorRateLimited, Operation: "applications.apply", Message: "slow down"}
	if err := store.Retry(ctx, lease, operationError, retryAt, now.Add(10*time.Second)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "worker", Now: retryAt.Add(-time.Second), LeaseDuration: time.Minute}); err != nil || found {
		t.Fatalf("retry claimed early: found=%v err=%v", found, err)
	}
	lease, found, err = store.Claim(ctx, broker.ClaimParams{WorkerID: "worker", Now: retryAt, LeaseDuration: time.Minute})
	if err != nil || !found || lease.Task.Attempts != 2 {
		t.Fatalf("claim retry: found=%v lease=%#v err=%v", found, lease, err)
	}
	if err := store.Fail(ctx, lease, &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "applications.apply"}, retryAt.Add(time.Second)); err != nil {
		t.Fatalf("fail: %v", err)
	}

	deadline := now.Add(10 * time.Minute)
	if _, err := store.Enqueue(ctx, sqliteTask(t, "task-deadline", "key-deadline", now, &deadline)); err != nil {
		t.Fatalf("enqueue deadline: %v", err)
	}
	if _, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "worker", Now: deadline, LeaseDuration: time.Minute}); err != nil || found {
		t.Fatalf("expired task claimed: found=%v err=%v", found, err)
	}
	stored, err := store.TaskByIdempotencyKey(ctx, "key-deadline")
	if err != nil || stored.Status != core.TaskFailed || stored.Failure == nil || stored.Failure.Message != "task deadline expired" {
		t.Fatalf("deadline task: %#v err=%v", stored, err)
	}
}

func TestStoreAuthWarningProfiles(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 17, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-auth", Type: core.TaskApplicationSubmit, IdempotencyKey: "key-auth",
		Source: "test", Platform: "hh", ProfileID: "primary",
		CorrelationID: "correlation-auth", Payload: json.RawMessage(`{}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if _, err := store.Enqueue(ctx, task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	lease, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "worker", Now: now, LeaseDuration: time.Minute})
	if err != nil || !found {
		t.Fatalf("claim: found=%v err=%v", found, err)
	}
	if err := store.Fail(ctx, lease, &core.OperationError{Category: core.ErrorUnauthorized, Operation: "application.submit"}, now.Add(time.Second)); err != nil {
		t.Fatalf("fail: %v", err)
	}
	warnings, err := store.AuthWarningProfiles(ctx, now.Add(-time.Hour))
	if err != nil || len(warnings) != 1 || warnings[0].ProfileID != "primary" || warnings[0].Count != 1 {
		t.Fatalf("warnings=%#v err=%v", warnings, err)
	}
	if stale, err := store.AuthWarningProfiles(ctx, now.Add(time.Hour)); err != nil || len(stale) != 0 {
		t.Fatalf("window was not applied: %#v err=%v", stale, err)
	}
}

func sqliteTask(t *testing.T, id core.TaskID, key string, now time.Time, deadline *time.Time) core.Task {
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

func TestFailedAuthTasksListsOnlyUnauthorizedFailuresOfTheProfile(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 18, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	unauthorized := sqliteTask(t, "task-auth", "key-auth", now, nil)
	unauthorized.Type, unauthorized.ProfileID = core.TaskConversationDiscover, "main"
	temporary := sqliteTask(t, "task-temp", "key-temp", now, nil)
	temporary.Type, temporary.ProfileID = core.TaskConversationSync, "main"
	foreign := sqliteTask(t, "task-other", "key-other", now, nil)
	foreign.ProfileID = "secondary"
	for _, task := range []core.Task{unauthorized, temporary, foreign} {
		if _, err := store.Enqueue(ctx, task); err != nil {
			t.Fatalf("enqueue %s: %v", task.ID, err)
		}
	}
	fail := func(taskType core.TaskType, category core.ErrorCategory) {
		t.Helper()
		lease, found, err := store.Claim(ctx, broker.ClaimParams{
			WorkerID: "worker", TaskType: taskType, Now: now.Add(time.Second), LeaseDuration: time.Minute,
		})
		if err != nil || !found {
			t.Fatalf("claim %s: found=%t err=%v", taskType, found, err)
		}
		if err := store.Fail(ctx, lease, &core.OperationError{
			Category: category, Operation: string(taskType), Message: "failed",
		}, now.Add(2*time.Second)); err != nil {
			t.Fatalf("fail %s: %v", taskType, err)
		}
	}
	fail(core.TaskConversationDiscover, core.ErrorUnauthorized)
	fail(core.TaskConversationSync, core.ErrorTemporaryFailure)
	fail(core.TaskApplicationSubmit, core.ErrorUnauthorized)

	items, err := store.FailedAuthTasks(ctx, "main", 10)
	if err != nil {
		t.Fatalf("failed auth tasks: %v", err)
	}
	if len(items) != 1 || items[0].ID != unauthorized.ID {
		t.Fatalf("failed auth tasks = %#v", items)
	}
}

func TestTaskRetryPreservesAttemptsForPacing(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	task := sqliteTask(t, "task-pacing", "key-pacing", now, nil)
	if created, err := store.Enqueue(ctx, task); err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	lease, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "worker-a", Now: now, LeaseDuration: time.Minute})
	if err != nil || !found || lease.Task.Attempts != 1 {
		t.Fatalf("claim: found=%v attempts=%d err=%v", found, lease.Task.Attempts, err)
	}
	retryAt := now.Add(time.Minute)
	pacing := &core.OperationError{
		Category: core.ErrorRateLimited, Operation: "applications.pacing.wait",
		Message: "application submit is waiting for its pacing slot", RetryAfter: &retryAt,
		Metadata: map[string]string{"pacing": "true"},
	}
	if err := store.Retry(ctx, lease, pacing, retryAt, now.Add(time.Second)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	second, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "worker-a", Now: retryAt.Add(time.Second), LeaseDuration: time.Minute})
	if err != nil || !found || second.Task.Attempts != 1 {
		t.Fatalf("pacing wait burned an attempt: found=%v attempts=%d err=%v", found, second.Task.Attempts, err)
	}
	realFailure := &core.OperationError{Category: core.ErrorTemporaryFailure, Operation: "applications.submit", Message: "platform hiccup"}
	if err := store.Retry(ctx, second, realFailure, retryAt.Add(time.Minute), retryAt.Add(2*time.Second)); err != nil {
		t.Fatalf("second retry: %v", err)
	}
	third, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "worker-a", Now: retryAt.Add(2 * time.Minute), LeaseDuration: time.Minute})
	if err != nil || !found || third.Task.Attempts != 2 {
		t.Fatalf("real failure must burn an attempt: found=%v attempts=%d err=%v", found, third.Task.Attempts, err)
	}
}
