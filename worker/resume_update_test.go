package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

type fakeResumePlanner struct {
	resource core.ProfileStateResource
	proposal core.ProfileStateProposal
	created  bool
}

func (planner fakeResumePlanner) Resource(tag string) (core.ProfileStateResource, bool) {
	if planner.resource.Tag != tag {
		return core.ProfileStateResource{}, false
	}
	return planner.resource, true
}

func (planner fakeResumePlanner) ReadAndPlan(context.Context, string, adapter.ProfileStateReader) (core.ProfileStateProposal, bool, error) {
	return planner.proposal, planner.created, nil
}

type fakeResumeStateReader struct{}

func (fakeResumeStateReader) ReadProfileState(context.Context, adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
	return core.ProfileStateObservation{}, nil
}

type fakeResumeStateWriter struct{ applies int }

func (writer *fakeResumeStateWriter) ApplyProfileState(context.Context, core.ProfileStateProposal) (adapter.ProfileStateApplyResult, error) {
	writer.applies++
	return adapter.ProfileStateApplyResult{}, nil
}

type fakeResumeUpdatePublisher struct {
	commands []adapter.ResumePublishCommand
}

func (publisher *fakeResumeUpdatePublisher) PublishResume(_ context.Context, command adapter.ResumePublishCommand) (adapter.ResumePublishResult, error) {
	publisher.commands = append(publisher.commands, command)
	return adapter.ResumePublishResult{}, nil
}

func resumeUpdateFixture(t *testing.T, status core.ProfileStateProposalStatus) (*ResumeUpdateHandler, *fakeResumeStateWriter, *fakeResumeUpdatePublisher) {
	t.Helper()
	resource := core.ProfileStateResource{Tag: "primary-resume", ProfileID: "primary"}
	planner := fakeResumePlanner{
		resource: resource,
		proposal: core.ProfileStateProposal{ID: "proposal-1", ResourceTag: resource.Tag, ProfileID: "primary", Status: status},
		created:  true,
	}
	writer := &fakeResumeStateWriter{}
	writers := NewProfileStateWriterRegistry()
	if err := writers.Register("primary", writer); err != nil {
		t.Fatalf("register writer: %v", err)
	}
	publisher := &fakeResumeUpdatePublisher{}
	publishers := NewResumePublisherRegistry()
	if err := publishers.Register("primary", publisher); err != nil {
		t.Fatalf("register publisher: %v", err)
	}
	handler, err := NewResumeUpdateHandler(planner, map[core.ProfileID]adapter.ProfileStateReader{"primary": fakeResumeStateReader{}}, writers, publishers)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return handler, writer, publisher
}

func resumeUpdateTask(t *testing.T, publish bool) core.Task {
	t.Helper()
	payload, err := json.Marshal(core.ResumeUpdatePayload{
		ProfileID: "primary", ResourceTag: "primary-resume", ResumeID: "resume-1", Publish: publish,
	})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return core.Task{
		ID: "update-task", Type: core.TaskResumeUpdate, ProfileID: "primary",
		CorrelationID: "correlation-1", IdempotencyKey: "update-1", Payload: payload,
	}
}

func TestResumeUpdateAppliesAndPublishes(t *testing.T) {
	handler, writer, publisher := resumeUpdateFixture(t, core.ProfileStateProposalPlanned)
	if err := handler.Handle(context.Background(), resumeUpdateTask(t, true)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if writer.applies != 1 || len(publisher.commands) != 1 {
		t.Fatalf("writer=%d published=%#v", writer.applies, publisher.commands)
	}
	if publisher.commands[0].ResumeID != "resume-1" || publisher.commands[0].IdempotencyKey != "update-1:publish" {
		t.Fatalf("publish command = %#v", publisher.commands[0])
	}
}

func TestResumeUpdateNoChangeSkipsApplyAndPublish(t *testing.T) {
	handler, writer, publisher := resumeUpdateFixture(t, core.ProfileStateProposalNoChanges)
	if err := handler.Handle(context.Background(), resumeUpdateTask(t, false)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if writer.applies != 0 || len(publisher.commands) != 0 {
		t.Fatalf("writer=%d published=%#v", writer.applies, publisher.commands)
	}
}

// A publish retry re-plans the already applied state as no_changes. The
// requested publish must still be attempted so a rate-limited raise is
// deferred and repeated instead of being silently dropped.
func TestResumeUpdateNoChangeStillPublishesWhenRequested(t *testing.T) {
	handler, writer, publisher := resumeUpdateFixture(t, core.ProfileStateProposalNoChanges)
	if err := handler.Handle(context.Background(), resumeUpdateTask(t, true)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if writer.applies != 0 {
		t.Fatalf("writer applies = %d, want 0", writer.applies)
	}
	if len(publisher.commands) != 1 || publisher.commands[0].ResumeID != "resume-1" {
		t.Fatalf("published=%#v", publisher.commands)
	}
	if err := handler.Handle(context.Background(), resumeUpdateTask(t, false)); err != nil {
		t.Fatalf("handle without publish: %v", err)
	}
	if len(publisher.commands) != 1 {
		t.Fatalf("publish without request = %#v", publisher.commands)
	}
}

func TestResumeUpdateRejectsUndeclaredResource(t *testing.T) {
	handler, _, _ := resumeUpdateFixture(t, core.ProfileStateProposalPlanned)
	task := resumeUpdateTask(t, false)
	task.Payload = []byte(`{"profile_id":"primary","resource_tag":"missing"}`)
	if err := handler.Handle(context.Background(), task); err == nil {
		t.Fatal("expected undeclared resource to fail")
	}
}
