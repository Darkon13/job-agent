package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestStoreFailsOpenAuthSessionsOnRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	waiting := core.AuthSession{
		ID: "auth-1", Platform: "hh", ProfileID: "primary", Status: core.AuthSessionCreated,
		Revision: 1, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateAuthSession(ctx, waiting); err != nil {
		t.Fatalf("create open session: %v", err)
	}
	completed := waiting
	completed.ID = "auth-2"
	completed.Status = core.AuthSessionCompleted
	completed.CredentialReference = "cred-1"
	completed.CredentialRevision = 1
	if _, _, err := store.CreateAuthSession(ctx, completed); err != nil {
		t.Fatalf("create completed session: %v", err)
	}
	recovered, err := store.FailOpenAuthSessions(ctx, now.Add(time.Minute), "backend restarted; start a new login")
	if err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	stored, err := store.AuthSession(ctx, "auth-1")
	if err != nil {
		t.Fatalf("load open session: %v", err)
	}
	if stored.Status != core.AuthSessionFailed || stored.Revision != 2 || stored.FailureCategory != core.ErrorTemporaryFailure {
		t.Fatalf("stored = %#v", stored)
	}
	done, err := store.AuthSession(ctx, "auth-2")
	if err != nil || done.Status != core.AuthSessionCompleted {
		t.Fatalf("completed session = %#v err=%v", done, err)
	}
	recovered, err = store.FailOpenAuthSessions(ctx, now.Add(2*time.Minute), "again")
	if err != nil || recovered != 0 {
		t.Fatalf("second pass recovered=%d err=%v", recovered, err)
	}
}
