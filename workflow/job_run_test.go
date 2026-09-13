package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
)

func TestJobRunWorkflowListsAndRunsConfiguredCommandsIdempotently(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	queue := brokermemory.NewQueue()
	clock := &mutableClock{now: now}
	definitions := []JobRunDefinition{
		{Tag: "sync", Commands: []JobRunCommand{
			{TaskType: core.TaskConversationDiscover, Platform: "hh", ProfileID: "primary", Payload: json.RawMessage(`{"profile_id":"primary"}`), Priority: 20},
		}},
		// One job with an array of profiles runs one task per profile.
		{Tag: "apply", Commands: []JobRunCommand{
			{TaskType: core.TaskApplicationCampaign, Platform: "hh", ProfileID: "primary", Payload: json.RawMessage(`{"job_tag":"apply","profiles":["primary"]}`), Priority: 90},
			{TaskType: core.TaskApplicationCampaign, Platform: "hh", ProfileID: "secondary", Payload: json.RawMessage(`{"job_tag":"apply","profiles":["secondary"]}`), Priority: 90},
		}},
	}
	workflow, err := NewJobRunWorkflow(queue, clock, &sequentialIDs{}, definitions)
	if err != nil {
		t.Fatalf("new job run workflow: %v", err)
	}
	listed := workflow.Definitions()
	if len(listed) != 2 || listed[0].Tag != "apply" || listed[1].Tag != "sync" || listed[0].Priority != 90 {
		t.Fatalf("unexpected definitions: %#v", listed)
	}
	if len(listed[0].Profiles) != 2 || listed[0].ProfileID != "primary" {
		t.Fatalf("multi-profile descriptor: %#v", listed[0])
	}

	task, created, err := workflow.Run(context.Background(), "apply", "dashboard-request-1")
	if err != nil || !created || task.Type != core.TaskApplicationCampaign || task.Priority != 90 || task.AvailableAt != now {
		t.Fatalf("run job: task=%#v created=%t err=%v", task, created, err)
	}
	if len(queue.Tasks()) != 2 {
		t.Fatalf("manual run created %d tasks, want 2: %#v", len(queue.Tasks()), queue.Tasks())
	}
	again, created, err := workflow.Run(context.Background(), "apply", "dashboard-request-1")
	if err != nil || created || again.ID != task.ID {
		t.Fatalf("repeat job: task=%#v created=%t err=%v", again, created, err)
	}
	if len(queue.Tasks()) != 2 {
		t.Fatalf("repeat run duplicated tasks: %#v", queue.Tasks())
	}
	other, created, err := workflow.Run(context.Background(), "sync", "dashboard-request-1")
	if err != nil || !created || other.ID == task.ID {
		t.Fatalf("same request key for another job: task=%#v created=%t err=%v", other, created, err)
	}
}

func TestJobRunWorkflowRejectsUnknownAndIncompleteJobs(t *testing.T) {
	queue := brokermemory.NewQueue()
	clock := &mutableClock{now: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
	workflow, err := NewJobRunWorkflow(queue, clock, &sequentialIDs{}, nil)
	if err != nil {
		t.Fatalf("new empty workflow: %v", err)
	}
	if _, _, err := workflow.Run(context.Background(), "missing", "request-1"); !errors.Is(err, ErrJobRunNotFound) {
		t.Fatalf("expected missing job error, got %v", err)
	}
	_, err = NewJobRunWorkflow(queue, clock, &sequentialIDs{}, []JobRunDefinition{
		{Tag: "job", Commands: []JobRunCommand{
			{TaskType: core.TaskResumeTouch, Platform: "hh", ProfileID: "primary", Payload: json.RawMessage(`{}`)},
			{TaskType: core.TaskConversationDiscover, Platform: "hh", ProfileID: "primary", Payload: json.RawMessage(`{}`)},
		}},
	})
	if err == nil {
		t.Fatal("expected a job with mixed command types to fail")
	}
	if _, err := NewJobRunWorkflow(queue, clock, &sequentialIDs{}, []JobRunDefinition{{Tag: "empty"}}); err == nil {
		t.Fatal("expected a job without commands to fail")
	}
}
