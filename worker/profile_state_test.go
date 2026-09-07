package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage/memory"
)

type fakeProfileStateWriter struct {
	proposal core.ProfileStateProposal
	result   adapter.ProfileStateApplyResult
	err      error
}

func (writer *fakeProfileStateWriter) ApplyProfileState(_ context.Context, proposal core.ProfileStateProposal) (adapter.ProfileStateApplyResult, error) {
	writer.proposal = proposal
	return writer.result, writer.err
}

func TestProfileStateApplyHandlerLoadsDesiredSnapshotOutsideTask(t *testing.T) {
	now := time.Date(2026, 9, 7, 22, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"desired secret"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	before, err := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"about":"old secret"}}}`), "", now)
	if err != nil {
		t.Fatalf("new before observation: %v", err)
	}
	proposal, err := core.NewProfileStateProposal("proposal-1", resource, before, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	repository := memory.NewRepository()
	if _, _, err := repository.CreateProfileStateProposal(context.Background(), proposal); err != nil {
		t.Fatalf("store proposal: %v", err)
	}
	after, err := core.NewProfileStateObservation("primary", resource.State, "", now.Add(time.Second))
	if err != nil {
		t.Fatalf("new after observation: %v", err)
	}
	writer := &fakeProfileStateWriter{result: adapter.ProfileStateApplyResult{Observation: after}}
	registry := NewProfileStateWriterRegistry()
	if err := registry.Register("primary", writer); err != nil {
		t.Fatalf("register writer: %v", err)
	}
	handler, err := NewProfileStateApplyHandler(repository, registry)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	payload, _ := json.Marshal(core.ProfileStateApplyPayload{ProposalID: proposal.ID})
	task := core.Task{Type: core.TaskProfileStateApply, ProfileID: "primary", Payload: payload}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle apply: %v", err)
	}
	if writer.proposal.ID != proposal.ID || string(writer.proposal.DesiredState) != string(proposal.DesiredState) {
		t.Fatalf("writer proposal = %#v", writer.proposal)
	}
	if string(task.Payload) != `{"proposal_id":"proposal-1"}` {
		t.Fatalf("task leaked desired snapshot: %s", task.Payload)
	}
}

func TestProfileStateApplyHandlerRejectsUnverifiedWriterResult(t *testing.T) {
	now := time.Now().UTC()
	resource, _ := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"new"}}}`))
	before, _ := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"about":"old"}}}`), "", now)
	proposal, _ := core.NewProfileStateProposal("proposal-1", resource, before, now)
	repository := memory.NewRepository()
	_, _, _ = repository.CreateProfileStateProposal(context.Background(), proposal)
	writer := &fakeProfileStateWriter{result: adapter.ProfileStateApplyResult{Observation: before}}
	registry := NewProfileStateWriterRegistry()
	_ = registry.Register("primary", writer)
	handler, _ := NewProfileStateApplyHandler(repository, registry)
	payload, _ := json.Marshal(core.ProfileStateApplyPayload{ProposalID: proposal.ID})
	err := handler.Handle(context.Background(), core.Task{Type: core.TaskProfileStateApply, ProfileID: "primary", Payload: payload})
	if !core.ErrorIsCategory(err, core.ErrorAmbiguousResult) {
		t.Fatalf("error = %v, want ambiguous result", err)
	}
}
