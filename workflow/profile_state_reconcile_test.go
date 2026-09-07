package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage/memory"
)

func TestProfileStateReconcileEnqueuesIdempotentlyWithoutDesiredValues(t *testing.T) {
	now := time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)
	resource := reconcileResource(t)
	planner, _ := NewProfileStatePlanner([]core.ProfileStateResource{resource}, memory.NewRepository(), fixedClock{now}, &sequentialIDs{})
	queue := brokermemory.NewQueue()
	workflow, err := NewProfileStateReconcileWorkflow(planner, queue, fixedClock{now}, &sequentialIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	first, created, err := workflow.Enqueue(context.Background(), resource.Tag, "api", "request-1")
	if err != nil || !created {
		t.Fatalf("first enqueue: %#v created=%v err=%v", first, created, err)
	}
	second, created, err := workflow.Enqueue(context.Background(), resource.Tag, "api", "request-1")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second enqueue: %#v created=%v err=%v", second, created, err)
	}
	if first.Type != core.TaskProfileStateReconcile || strings.Contains(string(first.Payload), "desired secret") || string(first.Payload) != `{"resource_tag":"primary-about"}` {
		t.Fatalf("unexpected reconcile task: %#v", first)
	}
}

func TestProfileStateReconcileHandlerPlansAndEnqueuesApply(t *testing.T) {
	now := time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)
	resource := reconcileResource(t)
	repository := memory.NewRepository()
	queue := brokermemory.NewQueue()
	planner, _ := NewProfileStatePlanner([]core.ProfileStateResource{resource}, repository, fixedClock{now}, &sequentialIDs{})
	apply, _ := NewProfileStateApplyWorkflow(repository, queue, fixedClock{now}, &sequentialIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	reader := profileStateReaderFunc(func(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		return core.NewProfileStateObservation(request.ProfileID, json.RawMessage(`{"resumes":{"resume-1":{"about":"current"}}}`), "", now)
	})
	handler, err := NewProfileStateReconcileHandler(planner, apply, map[core.ProfileID]adapter.ProfileStateReader{"primary": reader})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	task := reconcileTask(t, now)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle reconcile: %v", err)
	}
	var applyTask core.Task
	for _, candidate := range queue.Tasks() {
		if candidate.Type == core.TaskProfileStateApply {
			applyTask = candidate
		}
	}
	if applyTask.ID == "" || strings.Contains(string(applyTask.Payload), "desired secret") {
		t.Fatalf("apply task = %#v", applyTask)
	}
	var payload core.ProfileStateApplyPayload
	if err := json.Unmarshal(applyTask.Payload, &payload); err != nil || payload.ProposalID == "" {
		t.Fatalf("apply payload = %#v err=%v", payload, err)
	}
	proposal, err := repository.ProfileStateProposal(context.Background(), payload.ProposalID)
	if err != nil || !strings.Contains(string(proposal.DesiredState), "desired secret") {
		t.Fatalf("proposal desired state = %s err=%v", proposal.DesiredState, err)
	}
}

func TestProfileStateReconcileHandlerCompletesWhenAlreadyConverged(t *testing.T) {
	now := time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)
	resource := reconcileResource(t)
	repository := memory.NewRepository()
	queue := brokermemory.NewQueue()
	planner, _ := NewProfileStatePlanner([]core.ProfileStateResource{resource}, repository, fixedClock{now}, &sequentialIDs{})
	apply, _ := NewProfileStateApplyWorkflow(repository, queue, fixedClock{now}, &sequentialIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	reader := profileStateReaderFunc(func(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		return core.NewProfileStateObservation(request.ProfileID, resource.State, "", now)
	})
	handler, _ := NewProfileStateReconcileHandler(planner, apply, map[core.ProfileID]adapter.ProfileStateReader{"primary": reader})
	if err := handler.Handle(context.Background(), reconcileTask(t, now)); err != nil {
		t.Fatalf("handle converged resource: %v", err)
	}
	if len(queue.Tasks()) != 0 {
		t.Fatalf("unexpected apply tasks: %#v", queue.Tasks())
	}
}

func reconcileResource(t *testing.T) core.ProfileStateResource {
	t.Helper()
	resource, err := core.NewProfileStateResource("primary-about", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"desired secret"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	return resource
}

func reconcileTask(t *testing.T, now time.Time) core.Task {
	t.Helper()
	payload, _ := json.Marshal(core.ProfileStateReconcilePayload{ResourceTag: "primary-about"})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "reconcile-task", Type: core.TaskProfileStateReconcile, IdempotencyKey: "reconcile-1",
		Source: "test", Platform: "hh", ProfileID: "primary", CorrelationID: "correlation-1", Payload: payload,
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	return task
}
