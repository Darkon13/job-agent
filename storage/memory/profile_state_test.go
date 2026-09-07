package memory

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func profileStateProposalFixture(t *testing.T) core.ProfileStateProposal {
	t.Helper()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("primary", "profile-1", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"backend":{"about":"new"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	observation, err := core.NewProfileStateObservation("profile-1", json.RawMessage(`{"resumes":{"backend":{"about":"old"}}}`), "remote-1", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	proposal, err := core.NewProfileStateProposal("proposal-1", resource, observation, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	return proposal
}

func TestProfileStateProposalRepositoryIsIdempotentAndClones(t *testing.T) {
	repository := NewRepository()
	proposal := profileStateProposalFixture(t)
	stored, created, err := repository.CreateProfileStateProposal(context.Background(), proposal)
	if err != nil || !created {
		t.Fatalf("create proposal: stored=%#v created=%v err=%v", stored, created, err)
	}
	repeated := proposal
	repeated.ID = "proposal-retry"
	stored, created, err = repository.CreateProfileStateProposal(context.Background(), repeated)
	if err != nil || created || stored.ID != proposal.ID {
		t.Fatalf("repeat proposal: stored=%#v created=%v err=%v", stored, created, err)
	}
	stored.DesiredState[0] = '['
	stored.Changes[0].Path = "/mutated"
	loaded, err := repository.ProfileStateProposal(context.Background(), proposal.ID)
	if err != nil {
		t.Fatalf("load proposal: %v", err)
	}
	if string(loaded.DesiredState) != string(proposal.DesiredState) || loaded.Changes[0].Path == "/mutated" {
		t.Fatalf("stored proposal was mutated through returned clone: %#v", loaded)
	}
	listed, err := repository.ListProfileStateProposals(context.Background(), storage.ProfileStateProposalFilter{ProfileID: "profile-1", Status: core.ProfileStateProposalPlanned})
	if err != nil || len(listed) != 1 {
		t.Fatalf("list proposals: %#v err=%v", listed, err)
	}
}
