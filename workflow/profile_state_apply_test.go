package workflow

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
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

func profileStateApplyProposal(t *testing.T, repository *storagememory.Repository, before, desired string) core.ProfileStateProposal {
	t.Helper()
	now := time.Date(2026, 9, 7, 21, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":`+desired+`}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	observation, err := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"about":`+before+`}}}`), "", now)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	proposal, err := core.NewProfileStateProposal("proposal-1", resource, observation, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	if _, _, err := repository.CreateProfileStateProposal(context.Background(), proposal); err != nil {
		t.Fatalf("store proposal: %v", err)
	}
	return proposal
}

func TestProfileStateApplyWorkflowEnqueuesProposalReferenceIdempotently(t *testing.T) {
	repository := storagememory.NewRepository()
	proposal := profileStateApplyProposal(t, repository, `"old secret"`, `"desired secret"`)
	queue := brokermemory.NewQueue()
	workflow, err := NewProfileStateApplyWorkflow(repository, queue, fixedClock{proposal.CreatedAt}, &sequentialIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	first, created, err := workflow.Enqueue(context.Background(), proposal.ID, "api")
	if err != nil || !created {
		t.Fatalf("first enqueue: %#v created=%v err=%v", first, created, err)
	}
	second, created, err := workflow.Enqueue(context.Background(), proposal.ID, "api")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second enqueue: %#v created=%v err=%v", second, created, err)
	}
	if first.Type != core.TaskProfileStateApply || first.ProfileID != "primary" || first.Platform != "hh" ||
		first.Priority != core.TaskPriorityProfileStateApply || strings.Contains(string(first.Payload), "secret") {
		t.Fatalf("task = %#v", first)
	}
	var payload core.ProfileStateApplyPayload
	if err := json.Unmarshal(first.Payload, &payload); err != nil || payload.ProposalID != proposal.ID {
		t.Fatalf("payload = %#v err=%v", payload, err)
	}
}

func TestProfileStateApplyWorkflowRejectsNoChangesAndUnavailableWriter(t *testing.T) {
	repository := storagememory.NewRepository()
	noChanges := profileStateApplyProposal(t, repository, `"same"`, `"same"`)
	queue := brokermemory.NewQueue()
	workflow, err := NewProfileStateApplyWorkflow(repository, queue, fixedClock{noChanges.CreatedAt}, &sequentialIDs{}, nil)
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	if _, _, err := workflow.Enqueue(context.Background(), noChanges.ID, "api"); !errors.Is(err, ErrProfileStateNoChanges) {
		t.Fatalf("no changes error = %v", err)
	}

	repository = storagememory.NewRepository()
	changed := profileStateApplyProposal(t, repository, `"old"`, `"new"`)
	workflow, err = NewProfileStateApplyWorkflow(repository, queue, fixedClock{changed.CreatedAt}, &sequentialIDs{}, nil)
	if err != nil {
		t.Fatalf("new unavailable workflow: %v", err)
	}
	if _, _, err := workflow.Enqueue(context.Background(), changed.ID, "api"); !errors.Is(err, ErrProfileStateWriterUnavailable) {
		t.Fatalf("unavailable error = %v", err)
	}
}

func TestProfileStateApplyWorkflowControlsFailedTaskExplicitly(t *testing.T) {
	ctx := context.Background()
	repository := storagememory.NewRepository()
	proposal := profileStateApplyProposal(t, repository, `"old"`, `"new"`)
	queue := brokermemory.NewQueue()
	clock := fixedClock{proposal.CreatedAt}
	workflow, err := NewProfileStateApplyWorkflow(repository, queue, clock, &sequentialIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	task, created, err := workflow.Enqueue(ctx, proposal.ID, "api")
	if err != nil || !created {
		t.Fatalf("enqueue: task=%#v created=%t err=%v", task, created, err)
	}
	failTask := func() {
		lease, found, err := queue.Claim(ctx, broker.ClaimParams{
			WorkerID: "profile-worker", TaskType: core.TaskProfileStateApply,
			Now: clock.now, LeaseDuration: time.Minute,
		})
		if err != nil || !found {
			t.Fatalf("claim apply: found=%t err=%v", found, err)
		}
		if err := queue.Fail(ctx, lease, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "profile_state.apply", Message: "invalid field",
		}, clock.now); err != nil {
			t.Fatalf("fail apply: %v", err)
		}
	}
	failTask()
	retried, err := workflow.Retry(ctx, proposal.ID)
	if err != nil || retried.Status != core.TaskNew || retried.Attempts != 0 {
		t.Fatalf("retry: task=%#v err=%v", retried, err)
	}
	failTask()
	dismissed, err := workflow.Dismiss(ctx, proposal.ID)
	if err != nil || dismissed.Status != core.TaskDismissed || dismissed.Failure == nil {
		t.Fatalf("dismiss: task=%#v err=%v", dismissed, err)
	}
}
