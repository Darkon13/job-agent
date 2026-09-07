package sqlite_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

func sqliteProfileStateProposalFixture(t *testing.T, id core.ProfileStateProposalID) core.ProfileStateProposal {
	t.Helper()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("primary-backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"backend":{"about":"new"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	observation, err := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"backend":{"about":"old"}}}`), "remote-1", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	proposal, err := core.NewProfileStateProposal(id, resource, observation, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	return proposal
}

func TestSQLiteProfileStateProposalSurvivesRestartAndDeduplicatesInputs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	proposal := sqliteProfileStateProposalFixture(t, "proposal-1")
	stored, created, err := store.CreateProfileStateProposal(ctx, proposal)
	if err != nil || !created || stored.ID != proposal.ID {
		t.Fatalf("create proposal: stored=%#v created=%v err=%v", stored, created, err)
	}
	retry := proposal
	retry.ID = "proposal-retry"
	stored, created, err = store.CreateProfileStateProposal(ctx, retry)
	if err != nil || created || stored.ID != proposal.ID {
		t.Fatalf("retry proposal: stored=%#v created=%v err=%v", stored, created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	loaded, err := reopened.ProfileStateProposal(ctx, proposal.ID)
	if err != nil || string(loaded.DesiredState) != string(proposal.DesiredState) || len(loaded.Changes) != 1 {
		t.Fatalf("load proposal: %#v err=%v", loaded, err)
	}
	listed, err := reopened.ListProfileStateProposals(ctx, storage.ProfileStateProposalFilter{ResourceTag: "primary-backend", Status: core.ProfileStateProposalPlanned})
	if err != nil || len(listed) != 1 {
		t.Fatalf("list proposals: %#v err=%v", listed, err)
	}
	stats, err := reopened.Stats(ctx)
	if err != nil || stats.ProfileStateProposals != 1 {
		t.Fatalf("proposal stats: %#v err=%v", stats, err)
	}
}
