package workflow

import (
	"context"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
)

func TestProfileImportWorkflowEnqueuesADurableImport(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	queue := brokermemory.NewQueue()
	workflow, err := NewProfileImportWorkflow(queue, fixedClock{now}, &sequentialIDs{}, map[core.ProfileID]core.Platform{"main": "hh"})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	if workflow.Available("secondary") {
		t.Fatal("a profile without a platform must not import")
	}
	task, created, err := workflow.Enqueue(ctx, "main", "dashboard-import", "request-1")
	if err != nil || !created {
		t.Fatalf("enqueue: created=%t err=%v", created, err)
	}
	if task.Type != core.TaskProfileStateImport || task.ProfileID != "main" || task.Platform != "hh" {
		t.Fatalf("task = %#v", task)
	}
	if _, created, err := workflow.Enqueue(ctx, "main", "dashboard-import", "request-1"); err != nil || created {
		t.Fatalf("duplicate import: created=%t err=%v", created, err)
	}
	if _, _, err := workflow.Enqueue(ctx, "main", "", "request-2"); err == nil {
		t.Fatal("import without a source must be rejected")
	}
}
