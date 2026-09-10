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
		{Tag: "sync", TaskType: core.TaskConversationDiscover, Platform: "hh", ProfileID: "primary", Payload: json.RawMessage(`{"profile_id":"primary"}`), Priority: 20},
		{Tag: "apply", TaskType: core.TaskApplicationCampaign, Platform: "hh", ProfileID: "primary", Payload: json.RawMessage(`{"job_tag":"apply"}`), Priority: 90},
		// A job with several cron triggers still represents one manual command.
		{Tag: "apply", TaskType: core.TaskApplicationCampaign, Platform: "hh", ProfileID: "primary", Payload: json.RawMessage(`{"job_tag":"apply"}`), Priority: 90},
	}
	workflow, err := NewJobRunWorkflow(queue, clock, &sequentialIDs{}, definitions)
	if err != nil {
		t.Fatalf("new job run workflow: %v", err)
	}
	listed := workflow.Definitions()
	if len(listed) != 2 || listed[0].Tag != "apply" || listed[1].Tag != "sync" || listed[0].Priority != 90 {
		t.Fatalf("unexpected definitions: %#v", listed)
	}

	task, created, err := workflow.Run(context.Background(), "apply", "dashboard-request-1")
	if err != nil || !created || task.Type != core.TaskApplicationCampaign || task.Priority != 90 || task.AvailableAt != now {
		t.Fatalf("run job: task=%#v created=%t err=%v", task, created, err)
	}
	again, created, err := workflow.Run(context.Background(), "apply", "dashboard-request-1")
	if err != nil || created || again.ID != task.ID {
		t.Fatalf("repeat job: task=%#v created=%t err=%v", again, created, err)
	}
	other, created, err := workflow.Run(context.Background(), "sync", "dashboard-request-1")
	if err != nil || !created || other.ID == task.ID {
		t.Fatalf("same request key for another job: task=%#v created=%t err=%v", other, created, err)
	}
}

func TestJobRunWorkflowRejectsUnknownAndConflictingJobs(t *testing.T) {
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
		{Tag: "job", TaskType: core.TaskResumeTouch, Platform: "hh", ProfileID: "primary", Payload: json.RawMessage(`{}`)},
		{Tag: "job", TaskType: core.TaskConversationDiscover, Platform: "hh", ProfileID: "primary", Payload: json.RawMessage(`{}`)},
	})
	if err == nil {
		t.Fatal("expected conflicting job definitions to fail")
	}
}
