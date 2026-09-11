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
	calls   int
}

func (publisher *fakeResumePublisher) PublishResume(_ context.Context, command adapter.ResumePublishCommand) (adapter.ResumePublishResult, error) {
	publisher.calls++
	publisher.command = command
	return adapter.ResumePublishResult{}, nil
}

func resumePublishTask(t *testing.T, profileID core.ProfileID, resumeID string) core.Task {
	t.Helper()
	payload, err := json.Marshal(core.ResumePublishPayload{ProfileID: profileID, ResumeID: resumeID})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-publish", Type: core.TaskResumePublish, IdempotencyKey: "publish-1", Source: "test",
		Platform: "hh", ProfileID: profileID, CorrelationID: "correlation-1", Payload: payload,
	}, time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	return task
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
	if err := handler.Handle(context.Background(), resumePublishTask(t, "primary", "resume-1")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if publisher.calls != 1 || publisher.command.ProfileID != "primary" ||
		publisher.command.ResumeID != "resume-1" || publisher.command.IdempotencyKey != "publish-1" {
		t.Fatalf("unexpected command: %#v", publisher.command)
	}
}

func TestResumePublishHandlerRejectsProfileMismatchAndUnknownProfile(t *testing.T) {
	registry := NewResumePublisherRegistry()
	if err := registry.Register("primary", &fakeResumePublisher{}); err != nil {
		t.Fatalf("register publisher: %v", err)
	}
	handler, err := NewResumePublishHandler(registry)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	task := resumePublishTask(t, "secondary", "resume-1")
	task.Payload = json.RawMessage(`{"profile_id":"primary","resume_id":"resume-1"}`)
	if err := handler.Handle(context.Background(), task); err == nil {
		t.Fatal("expected profile mismatch to fail")
	}
	if err := handler.Handle(context.Background(), resumePublishTask(t, "missing", "resume-1")); err == nil {
		t.Fatal("expected unknown profile to fail")
	}
	broken := resumePublishTask(t, "primary", "resume-1")
	broken.Payload = json.RawMessage(`{"profile_id":"","resume_id":""}`)
	if err := handler.Handle(context.Background(), broken); err == nil {
		t.Fatal("expected invalid payload to fail")
	}
	if _, err := NewResumePublishHandler(nil); err == nil {
		t.Fatal("expected nil registry to fail")
	}
}
