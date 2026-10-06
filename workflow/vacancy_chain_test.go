package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/broker"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
)

type advancingClock struct{ now time.Time }

func (clock *advancingClock) Now() time.Time { return clock.now }

func TestVacancyTestWorkflowRequeuesSettledCapture(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	clock := &advancingClock{now: now}
	queue := brokermemory.NewQueue()
	workflow, err := NewVacancyTestWorkflow(queue, clock, &sequentialIDs{})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	ctx := context.Background()
	key, err := core.TestCaptureIdempotencyKey("hh", "42", "primary")
	if err != nil {
		t.Fatalf("capture key: %v", err)
	}
	if created, err := workflow.EnqueueCapture(ctx, "primary", "hh", "42", "application-review"); err != nil || !created {
		t.Fatalf("capture: created=%v err=%v", created, err)
	}
	fail := func(at time.Time) {
		t.Helper()
		lease, found, err := queue.Claim(ctx, broker.ClaimParams{WorkerID: "worker", Now: at, LeaseDuration: time.Minute})
		if err != nil || !found {
			t.Fatalf("claim capture: found=%t err=%v", found, err)
		}
		if err := queue.Fail(ctx, lease, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "test.capture", Message: "no tasks",
		}, at.Add(time.Second)); err != nil {
			t.Fatalf("fail capture: %v", err)
		}
	}
	fail(now.Add(time.Second))
	clock.now = now.Add(3 * time.Second)
	if created, err := workflow.EnqueueCapture(ctx, "primary", "hh", "42", "application-review"); err != nil || !created {
		t.Fatalf("failed capture must be requeued: created=%v err=%v", created, err)
	}
	task, err := queue.TaskByIdempotencyKey(ctx, key)
	if err != nil || task.Status != core.TaskNew || task.Attempts != 0 || task.Failure != nil {
		t.Fatalf("requeued failed capture: task=%#v err=%v", task, err)
	}
	fail(now.Add(4 * time.Second))
	if _, err := queue.DismissFailedTask(ctx, key, now.Add(6*time.Second)); err != nil {
		t.Fatalf("dismiss capture: %v", err)
	}
	clock.now = now.Add(7 * time.Second)
	if created, err := workflow.EnqueueCapture(ctx, "primary", "hh", "42", "application-review"); err != nil || !created {
		t.Fatalf("dismissed capture must be requeued: created=%v err=%v", created, err)
	}
	task, err = queue.TaskByIdempotencyKey(ctx, key)
	if err != nil || task.Status != core.TaskNew || task.Failure != nil {
		t.Fatalf("requeued dismissed capture: task=%#v err=%v", task, err)
	}
}

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
