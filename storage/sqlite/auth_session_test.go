package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func sqliteAuthSession(t *testing.T, id core.AuthSessionID, now time.Time) core.AuthSession {
	t.Helper()
	session, err := core.NewAuthSession(core.NewAuthSessionParams{
		ID: id, Platform: "hh", ProfileID: "primary", ExpiresAt: now.Add(15 * time.Minute),
	}, now)
	if err != nil {
		t.Fatalf("new auth session: %v", err)
	}
	return session
}

func TestStorePersistsAuthSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	session := sqliteAuthSession(t, "auth-1", now)
	stored, created, err := store.CreateAuthSession(ctx, session)
	if err != nil || !created {
		t.Fatalf("create session: created=%v err=%v", created, err)
	}
	repeated, created, err := store.CreateAuthSession(ctx, session)
	if err != nil || created || repeated.Revision != session.Revision {
		t.Fatalf("repeat create: created=%v err=%v session=%#v", created, err, repeated)
	}
	if err := store.SaveAuthSession(ctx, session, session.Revision); !errors.Is(err, storage.ErrRevisionConflict) {
		t.Fatalf("stale save error = %v, want revision conflict", err)
	}

	expected := stored.Revision
	if err := stored.BeginIdentifier(now.Add(time.Second)); err != nil {
		t.Fatalf("begin identifier: %v", err)
	}
	if err := store.SaveAuthSession(ctx, stored, expected); err != nil {
		t.Fatalf("save identifier: %v", err)
	}
	expected = stored.Revision
	challenge := core.AuthChallenge{ID: "challenge-1", Kind: core.AuthChallengeCaptcha, MediaType: "image/png", Prompt: "Enter the code", Deadline: now.Add(5 * time.Minute)}
	if err := stored.RequireChallenge(challenge, now.Add(2*time.Second)); err != nil {
		t.Fatalf("require challenge: %v", err)
	}
	if err := store.SaveAuthSession(ctx, stored, expected); err != nil {
		t.Fatalf("save challenge: %v", err)
	}
	expected = stored.Revision
	if err := stored.BeginExchange(now.Add(3 * time.Second)); err != nil {
		t.Fatalf("begin exchange: %v", err)
	}
	if err := store.SaveAuthSession(ctx, stored, expected); err != nil {
		t.Fatalf("save exchange: %v", err)
	}
	expected = stored.Revision
	if err := stored.BeginStoring(now.Add(4 * time.Second)); err != nil {
		t.Fatalf("begin storing: %v", err)
	}
	if err := store.SaveAuthSession(ctx, stored, expected); err != nil {
		t.Fatalf("save storing: %v", err)
	}
	expected = stored.Revision
	if err := stored.Complete("file:/run/secrets/hh-primary.json", 2, now.Add(5*time.Second)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := store.SaveAuthSession(ctx, stored, expected); err != nil {
		t.Fatalf("save completed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := openStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.AuthSession(ctx, "auth-1")
	if err != nil || persisted.Status != core.AuthSessionCompleted ||
		persisted.CredentialReference != "file:/run/secrets/hh-primary.json" ||
		persisted.CredentialRevision != 2 || persisted.Challenge != nil || persisted.Revision != stored.Revision {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
}

func TestStorePersistsAuthChallengeDescriptor(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "job-agent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session := sqliteAuthSession(t, "auth-2", now)
	stored, _, err := store.CreateAuthSession(ctx, session)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	expected := stored.Revision
	if err := stored.BeginIdentifier(now.Add(time.Second)); err != nil {
		t.Fatalf("begin identifier: %v", err)
	}
	if err := store.SaveAuthSession(ctx, stored, expected); err != nil {
		t.Fatalf("save identifier: %v", err)
	}
	expected = stored.Revision
	challenge := core.AuthChallenge{ID: "challenge-2", Kind: core.AuthChallengeOTP, Prompt: "Enter the code", Deadline: now.Add(time.Minute)}
	if err := stored.RequireChallenge(challenge, now.Add(2*time.Second)); err != nil {
		t.Fatalf("require challenge: %v", err)
	}
	if err := store.SaveAuthSession(ctx, stored, expected); err != nil {
		t.Fatalf("save challenge: %v", err)
	}
	persisted, err := store.AuthSession(ctx, "auth-2")
	if err != nil || persisted.Status != core.AuthSessionWaitingOTP || persisted.Challenge == nil ||
		persisted.Challenge.ID != "challenge-2" || persisted.Challenge.Kind != core.AuthChallengeOTP ||
		!persisted.Challenge.Deadline.Equal(challenge.Deadline) {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
}

func TestStoreRejectsConflictingAndMissingAuthSessions(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "job-agent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.AuthSession(ctx, "missing"); !errors.Is(err, storage.ErrAuthSessionNotFound) {
		t.Fatalf("missing session error = %v", err)
	}
	if err := store.SaveAuthSession(ctx, sqliteAuthSession(t, "missing", now), 1); !errors.Is(err, storage.ErrAuthSessionNotFound) {
		t.Fatalf("save missing session error = %v", err)
	}
	session := sqliteAuthSession(t, "auth-3", now)
	if _, _, err := store.CreateAuthSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	conflict := session
	conflict.ProfileID = "secondary"
	if _, _, err := store.CreateAuthSession(ctx, conflict); err == nil {
		t.Fatal("expected conflicting session inputs to fail")
	}
	changed, err := core.NewAuthSession(core.NewAuthSessionParams{
		ID: "auth-3", Platform: "hh", ProfileID: "primary", ExpiresAt: now.Add(30 * time.Minute),
	}, now)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if _, _, err := store.CreateAuthSession(ctx, changed); err == nil {
		t.Fatal("expected conflicting session expiry to fail")
	}
}
