package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/broker"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

func TestApplicationRemovalAcceptsPreparedApplications(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 27, 22, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	application, err := core.NewApplication("application-ready", core.ApplicationKey{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, _, err := repository.CreateApplication(ctx, application); err != nil {
		t.Fatalf("create application: %v", err)
	}
	if err := application.Transition(core.ApplicationPreparing, now.Add(time.Second)); err != nil {
		t.Fatalf("transition to preparing: %v", err)
	}
	if err := application.Transition(core.ApplicationReady, now.Add(2*time.Second)); err != nil {
		t.Fatalf("transition to ready: %v", err)
	}
	if err := repository.SaveApplication(ctx, application, core.ApplicationNew); err != nil {
		t.Fatalf("save application: %v", err)
	}
	workflow, err := NewApplicationRemovalWorkflow(repository, queue, SystemClock{}, RandomIDGenerator{})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	task, created, err := workflow.EnqueueRemoval(ctx, core.ApplicationRemovePayload{
		ApplicationID: application.ID, Reason: core.ApplicationRemovalManual,
	}, "queue-leftovers")
	if err != nil || !created || task.Type != core.TaskApplicationRemove {
		t.Fatalf("enqueue removal: task=%#v created=%t err=%v", task, created, err)
	}
	if _, _, err := workflow.EnqueueRemoval(ctx, core.ApplicationRemovePayload{
		ApplicationID: "application-missing", Reason: core.ApplicationRemovalManual,
	}, "queue-leftovers-missing"); err == nil {
		t.Fatal("expected a missing application to fail")
	}
	var _ = broker.ErrTaskNotFound
}
