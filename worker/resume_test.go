package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

type fakeResumePublisher struct {
	command adapter.ResumePublishCommand
	err     error
}

func (publisher *fakeResumePublisher) PublishResume(_ context.Context, command adapter.ResumePublishCommand) (adapter.ResumePublishResult, error) {
	publisher.command = command
	return adapter.ResumePublishResult{}, publisher.err
}

func TestResumePublishHandlerRoutesProfileAndIdempotency(t *testing.T) {
	publisher := &fakeResumePublisher{}
	registry := NewResumePublisherRegistry()
	if err := registry.Register("primary", publisher); err != nil {
		t.Fatalf("register publisher: %v", err)
	}
	handler, err := NewResumePublishHandler(registry)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	payload, _ := json.Marshal(core.ResumePublishPayload{ProfileID: "primary", ResumeID: "resume-1"})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-1", Type: core.TaskResumePublish, IdempotencyKey: "publish-1", Source: "test",
		Platform: "hh", ProfileID: "primary", CorrelationID: "correlation-1", Payload: payload,
	}, time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if publisher.command.ProfileID != "primary" || publisher.command.ResumeID != "resume-1" || publisher.command.IdempotencyKey != "publish-1" {
		t.Fatalf("unexpected command: %#v", publisher.command)
	}
}
