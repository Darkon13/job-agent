package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func memoryAuthSession(t *testing.T, id core.AuthSessionID, now time.Time) core.AuthSession {
	t.Helper()
	session, err := core.NewAuthSession(core.NewAuthSessionParams{
		ID: id, Platform: "hh", ProfileID: "primary", ExpiresAt: now.Add(15 * time.Minute),
	}, now)
	if err != nil {
		t.Fatalf("new auth session: %v", err)
	}
	return session
}

func TestMemoryAuthSessionLifecycleAndCAS(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := NewRepository()
	session := memoryAuthSession(t, "auth-1", now)
	stored, created, err := repository.CreateAuthSession(ctx, session)
	if err != nil || !created {
		t.Fatalf("create session: created=%v err=%v", created, err)
	}
	if _, err := repository.AuthSession(ctx, "missing"); !errors.Is(err, storage.ErrAuthSessionNotFound) {
		t.Fatalf("missing session error = %v", err)
	}
	if err := repository.SaveAuthSession(ctx, session, session.Revision); !errors.Is(err, storage.ErrRevisionConflict) {
		t.Fatalf("stale save error = %v", err)
	}
	expected := stored.Revision
	if err := stored.BeginIdentifier(now.Add(time.Second)); err != nil {
		t.Fatalf("begin identifier: %v", err)
	}
	if err := repository.SaveAuthSession(ctx, stored, expected); err != nil {
		t.Fatalf("save identifier: %v", err)
	}
	persisted, err := repository.AuthSession(ctx, "auth-1")
	if err != nil || persisted.Status != core.AuthSessionWaitingIdentifier || persisted.Revision != stored.Revision {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	conflict := session
	conflict.ProfileID = "secondary"
	if _, _, err := repository.CreateAuthSession(ctx, conflict); err == nil {
		t.Fatal("expected conflicting session inputs to fail")
	}
}
