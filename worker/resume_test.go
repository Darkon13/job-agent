package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

type fakeResumeToucher struct{ command adapter.ResumeTouchCommand }

func (toucher *fakeResumeToucher) TouchResume(_ context.Context, command adapter.ResumeTouchCommand) (adapter.ResumeTouchResult, error) {
	toucher.command = command
	return adapter.ResumeTouchResult{}, nil
}

func TestResumeTouchHandlerRoutesProfileAndIdempotency(t *testing.T) {
	toucher := &fakeResumeToucher{}
	registry := NewResumeToucherRegistry()
	if err := registry.Register("primary", toucher); err != nil {
		t.Fatalf("register toucher: %v", err)
	}
	handler, err := NewResumeTouchHandler(registry)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	payload, _ := json.Marshal(core.ResumeTouchPayload{ProfileID: "primary", ResumeID: "resume-1"})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-1", Type: core.TaskResumeTouch, IdempotencyKey: "touch-1", Source: "test",
		Platform: "hh", ProfileID: "primary", CorrelationID: "correlation-1", Payload: payload,
	}, time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if toucher.command.ProfileID != "primary" || toucher.command.ResumeID != "resume-1" || toucher.command.IdempotencyKey != "touch-1" {
		t.Fatalf("unexpected command: %#v", toucher.command)
	}
}
