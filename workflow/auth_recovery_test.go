package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/broker"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/scheduler"
	"github.com/Darkon13/job-agent/storage"
)

type authGuardPauses struct {
	items   map[string]scheduler.Pause
	resumed []string
}

func (store *authGuardPauses) PauseJob(_ context.Context, jobTag string, profileID core.ProfileID, reason string, now time.Time) error {
	store.items[jobTag+"\x00"+string(profileID)] = scheduler.Pause{JobTag: jobTag, ProfileID: profileID, Reason: reason, CreatedAt: now}
	return nil
}

func (store *authGuardPauses) ResumeJob(_ context.Context, jobTag string, profileID core.ProfileID) (int, error) {
	key := jobTag + "\x00" + string(profileID)
	if _, exists := store.items[key]; !exists {
		return 0, nil
	}
	delete(store.items, key)
	store.resumed = append(store.resumed, key)
	return 1, nil
}

func (store *authGuardPauses) JobPauses(context.Context) ([]scheduler.Pause, error) {
	pauses := make([]scheduler.Pause, 0, len(store.items))
	for _, pause := range store.items {
		pauses = append(pauses, pause)
	}
	return pauses, nil
}

type authGuardFailures struct {
	items []storage.FailedTaskSummary
	asked []core.ProfileID
}

func (store *authGuardFailures) FailedAuthTasks(_ context.Context, profileID core.ProfileID, _ int) ([]storage.FailedTaskSummary, error) {
	store.asked = append(store.asked, profileID)
	return store.items, nil
}

func TestAuthGuardPausesTheFailingJobAndRecoversAfterLogin(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 18, 0, 0, 0, time.UTC)
	queue := brokermemory.NewQueue()
	control, err := NewTaskControlWorkflow(queue, fixedClock{now.Add(2 * time.Second)})
	if err != nil {
		t.Fatalf("task control: %v", err)
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-1", Type: core.TaskConversationDiscover, IdempotencyKey: "key-1",
		Source: "cron:system.state.chats", Platform: "hh", ProfileID: "main",
		CorrelationID: "corr-1", Payload: json.RawMessage(`{"profile_id":"main"}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if _, err := queue.Enqueue(ctx, task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	lease, found, err := queue.Claim(ctx, broker.ClaimParams{WorkerID: "worker", Now: now, LeaseDuration: time.Minute})
	if err != nil || !found {
		t.Fatalf("claim: found=%t err=%v", found, err)
	}
	if err := queue.Fail(ctx, lease, &core.OperationError{
		Category: core.ErrorUnauthorized, Operation: "conversation.discover", Message: "browser state is unavailable",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("fail task: %v", err)
	}

	pauses := &authGuardPauses{items: map[string]scheduler.Pause{}}
	failures := &authGuardFailures{items: []storage.FailedTaskSummary{{ID: "task-1", ProfileID: "main", Type: core.TaskConversationDiscover}}}
	guard, err := NewAuthGuard(pauses, failures, control, fixedClock{now.Add(2 * time.Second)})
	if err != nil {
		t.Fatalf("auth guard: %v", err)
	}

	if err := guard.Guard(ctx, task); err != nil {
		t.Fatalf("guard: %v", err)
	}
	pause, exists := pauses.items["system.state.chats\x00main"]
	if !exists || pause.Reason != AuthPauseReason {
		t.Fatalf("the failing job must be paused: %#v", pauses.items)
	}
	// A task without a job source has nothing to pause.
	if err := guard.Guard(ctx, core.Task{Source: "dashboard-questionnaire", ProfileID: "main"}); err != nil {
		t.Fatalf("guard without a job: %v", err)
	}
	if len(pauses.items) != 1 {
		t.Fatalf("unrelated sources must not pause jobs: %#v", pauses.items)
	}

	if err := guard.AuthCompleted(ctx, "main", ""); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if len(pauses.resumed) != 1 || pauses.resumed[0] != "system.state.chats\x00main" {
		t.Fatalf("the guarded job must be resumed: %#v", pauses.resumed)
	}
	if len(failures.asked) != 1 || failures.asked[0] != "main" {
		t.Fatalf("recovery must look at the signed-in profile: %#v", failures.asked)
	}
	retried, err := queue.TaskByIdempotencyKey(ctx, "key-1")
	if err != nil || retried.Status != core.TaskNew || retried.Failure != nil {
		t.Fatalf("the failed task must be retried: %#v err=%v", retried, err)
	}
}
