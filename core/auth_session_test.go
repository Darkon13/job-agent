package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func newAuthSessionFixture(t *testing.T) (AuthSession, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	session, err := NewAuthSession(NewAuthSessionParams{
		ID: "auth-1", Platform: "hh", ProfileID: "primary", ExpiresAt: now.Add(15 * time.Minute),
	}, now)
	if err != nil {
		t.Fatalf("new auth session: %v", err)
	}
	return session, now
}

func authChallenge(kind AuthChallengeKind, id string, deadline time.Time) AuthChallenge {
	mediaType := ""
	if kind == AuthChallengeCaptcha {
		mediaType = "image/png"
	}
	return AuthChallenge{ID: id, Kind: kind, MediaType: mediaType, Prompt: "Enter the code", Deadline: deadline}
}

func TestAuthSessionLifecycle(t *testing.T) {
	session, now := newAuthSessionFixture(t)
	if err := session.BeginIdentifier(now.Add(time.Second)); err != nil {
		t.Fatalf("begin identifier: %v", err)
	}
	if err := session.RequireChallenge(authChallenge(AuthChallengeOTP, "challenge-1", now.Add(5*time.Minute)), now.Add(2*time.Second)); err != nil {
		t.Fatalf("require challenge: %v", err)
	}
	if session.Status != AuthSessionWaitingOTP || session.Challenge == nil || session.Challenge.ID != "challenge-1" {
		t.Fatalf("session = %#v", session)
	}
	if err := session.BeginExchange(now.Add(3 * time.Second)); err != nil {
		t.Fatalf("begin exchange: %v", err)
	}
	if session.Challenge != nil || session.Status != AuthSessionExchanging {
		t.Fatalf("exchange session = %#v", session)
	}
	if err := session.BeginStoring(now.Add(4 * time.Second)); err != nil {
		t.Fatalf("begin storing: %v", err)
	}
	if err := session.Complete("file:/run/secrets/hh-primary.json", 1, now.Add(5*time.Second)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if session.Status != AuthSessionCompleted || session.CredentialReference == "" || session.CredentialRevision != 1 || session.Revision != 6 {
		t.Fatalf("completed session = %#v", session)
	}
	if !session.IsTerminal() {
		t.Fatal("completed session must be terminal")
	}
}

func TestAuthSessionCaptchaAndPasswordKinds(t *testing.T) {
	session, now := newAuthSessionFixture(t)
	if err := session.BeginIdentifier(now.Add(time.Second)); err != nil {
		t.Fatalf("begin identifier: %v", err)
	}
	if err := session.RequireChallenge(authChallenge(AuthChallengeCaptcha, "captcha-1", now.Add(time.Minute)), now.Add(2*time.Second)); err != nil {
		t.Fatalf("require captcha: %v", err)
	}
	if session.Status != AuthSessionWaitingCaptcha || !session.WaitingChallenge() {
		t.Fatalf("captcha session = %#v", session)
	}
	if err := session.RequireChallenge(authChallenge(AuthChallengePassword, "password-1", now.Add(2*time.Minute)), now.Add(3*time.Second)); err != nil {
		t.Fatalf("require password: %v", err)
	}
	if session.Status != AuthSessionWaitingPassword || session.Challenge.Kind != AuthChallengePassword {
		t.Fatalf("password session = %#v", session)
	}
}

func TestAuthSessionChallengeRetryIsIdempotent(t *testing.T) {
	session, now := newAuthSessionFixture(t)
	if err := session.BeginIdentifier(now.Add(time.Second)); err != nil {
		t.Fatalf("begin identifier: %v", err)
	}
	challenge := authChallenge(AuthChallengeOTP, "challenge-1", now.Add(time.Minute))
	if err := session.RequireChallenge(challenge, now.Add(2*time.Second)); err != nil {
		t.Fatalf("require challenge: %v", err)
	}
	revision := session.Revision
	if err := session.RequireChallenge(challenge, now.Add(3*time.Second)); err != nil {
		t.Fatalf("repeat challenge: %v", err)
	}
	if session.Revision != revision {
		t.Fatalf("revision changed on idempotent retry: %d -> %d", revision, session.Revision)
	}
}

func TestAuthSessionRejectsInvalidTransitions(t *testing.T) {
	session, now := newAuthSessionFixture(t)
	if err := session.BeginExchange(now.Add(time.Second)); err == nil {
		t.Fatal("expected exchange before identifier to fail")
	}
	if err := session.Complete("file:/x", 1, now.Add(time.Second)); err == nil {
		t.Fatal("expected complete before storing to fail")
	}
	if err := session.BeginIdentifier(now.Add(time.Second)); err != nil {
		t.Fatalf("begin identifier: %v", err)
	}
	if err := session.BeginIdentifier(now.Add(2 * time.Second)); err == nil {
		t.Fatal("expected second identifier step to fail")
	}
	late := authChallenge(AuthChallengeOTP, "challenge-late", now.Add(time.Hour))
	if err := session.RequireChallenge(late, now.Add(2*time.Second)); err == nil {
		t.Fatal("expected challenge after session expiry to fail")
	}
}

func TestAuthSessionExpiryCancelAndFailure(t *testing.T) {
	session, now := newAuthSessionFixture(t)
	if err := session.BeginIdentifier(now.Add(time.Second)); err != nil {
		t.Fatalf("begin identifier: %v", err)
	}
	if err := session.Expire(now.Add(time.Minute)); err == nil {
		t.Fatal("expected early expiry to fail")
	}
	if err := session.Expire(now.Add(20 * time.Minute)); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if session.Status != AuthSessionExpired || session.Challenge != nil {
		t.Fatalf("expired session = %#v", session)
	}
	if err := session.Cancel(now.Add(21 * time.Minute)); err == nil {
		t.Fatal("expected cancel after expiry to fail")
	}

	failed, now := newAuthSessionFixture(t)
	if err := failed.Fail(ErrorUnauthorized, "platform rejected the login", now.Add(time.Second)); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if failed.Status != AuthSessionFailed || failed.FailureCategory != ErrorUnauthorized {
		t.Fatalf("failed session = %#v", failed)
	}
	other, now := newAuthSessionFixture(t)
	if err := other.Fail("unknown", "", now.Add(time.Second)); err == nil {
		t.Fatal("expected unknown failure category to fail")
	}
	noisy, now := newAuthSessionFixture(t)
	if err := noisy.Fail(ErrorTemporaryFailure, "line\nbreak", now.Add(time.Second)); err != nil {
		t.Fatalf("newline failure: %v", err)
	}
	control, now := newAuthSessionFixture(t)
	if err := control.Fail(ErrorTemporaryFailure, "bad\x00value", now.Add(time.Second)); err == nil {
		t.Fatal("expected control character to fail")
	}
}

func TestAuthSessionPublicJSONDoesNotCarryInteractivePayload(t *testing.T) {
	session, now := newAuthSessionFixture(t)
	if err := session.BeginIdentifier(now.Add(time.Second)); err != nil {
		t.Fatalf("begin identifier: %v", err)
	}
	if err := session.RequireChallenge(authChallenge(AuthChallengeCaptcha, "captcha-1", now.Add(time.Minute)), now.Add(2*time.Second)); err != nil {
		t.Fatalf("require challenge: %v", err)
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	if strings.Contains(string(encoded), "answer") || strings.Contains(string(encoded), "payload") || strings.Contains(string(encoded), "token") {
		t.Fatalf("public session leaked interactive data: %s", encoded)
	}
}

func TestAuthSessionValidateRejectsInconsistentRecords(t *testing.T) {
	session, now := newAuthSessionFixture(t)
	session.Status = AuthSessionWaitingOTP
	if err := session.Validate(); err == nil {
		t.Fatal("expected waiting status without challenge to fail")
	}
	session, _ = newAuthSessionFixture(t)
	session.Status = AuthSessionCompleted
	if err := session.Validate(); err == nil {
		t.Fatal("expected completed status without credential to fail")
	}
	session, _ = newAuthSessionFixture(t)
	session.CredentialRevision = 2
	if err := session.Validate(); err == nil {
		t.Fatal("expected credential revision without reference to fail")
	}
	late, err := NewAuthSession(NewAuthSessionParams{
		ID: "auth-2", Platform: "hh", ProfileID: "primary", ExpiresAt: now,
	}, now)
	if err == nil {
		t.Fatal("expected session expiry equal to creation to fail")
	}
	if late.ID != "" {
		t.Fatalf("failed session = %#v", late)
	}
	noisy, _ := newAuthSessionFixture(t)
	if err := noisy.BeginIdentifier(now.Add(time.Second)); err != nil {
		t.Fatalf("begin identifier: %v", err)
	}
	bad := authChallenge(AuthChallengeOTP, "challenge-1", now.Add(time.Minute))
	bad.Prompt = strings.Repeat("x", maximumAuthChallengePromptRunes+1)
	if err := noisy.RequireChallenge(bad, now.Add(2*time.Second)); err == nil {
		t.Fatal("expected oversized challenge prompt to fail")
	}
}
