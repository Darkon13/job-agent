package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
)

func TestVacancyTestWorkflowEnqueuesIdempotentSteps(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	queue := brokermemory.NewQueue()
	workflow, err := NewVacancyTestWorkflow(queue, fixedClock{now: now}, &sequentialIDs{})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	ctx := context.Background()
	created, err := workflow.EnqueueCapture(ctx, "primary", "hh", "42", "application-review")
	if err != nil || !created {
		t.Fatalf("capture: created=%v err=%v", created, err)
	}
	if created, err := workflow.EnqueueCapture(ctx, "primary", "hh", "42", "application-submit"); err != nil || created {
		t.Fatalf("duplicate capture: created=%v err=%v", created, err)
	}
	key, err := core.TestCaptureIdempotencyKey("hh", "42", "primary")
	if err != nil {
		t.Fatalf("capture key: %v", err)
	}
	task, err := queue.TaskByIdempotencyKey(ctx, key)
	if err != nil {
		t.Fatalf("load capture task: %v", err)
	}
	var capture core.TestCapturePayload
	if err := json.Unmarshal(task.Payload, &capture); err != nil {
		t.Fatalf("decode capture payload: %v", err)
	}
	if task.Type != core.TaskTestCapture || capture.ProfileID != "primary" || capture.VacancyExternalID != "42" {
		t.Fatalf("capture task = %#v payload=%#v", task, capture)
	}

	answers := []core.ResolvedAnswer{{QuestionID: "1", Text: "Five years of Go"}}
	if created, err := workflow.EnqueueAnswer(ctx, "primary", "hh", "42", answers, "capture-1"); err != nil || !created {
		t.Fatalf("answer: created=%v err=%v", created, err)
	}
	if created, err := workflow.EnqueueAnswer(ctx, "primary", "hh", "42", answers, "capture-1"); err != nil || created {
		t.Fatalf("duplicate answer: created=%v err=%v", created, err)
	}
	if created, err := workflow.EnqueueComplete(ctx, "primary", "hh", "42", core.TestAttemptSubmitted, "", "answer-1"); err != nil || !created {
		t.Fatalf("complete: created=%v err=%v", created, err)
	}
}
