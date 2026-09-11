package auth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/credentials"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type authTestClock struct{ now time.Time }

func (clock *authTestClock) Now() time.Time { return clock.now }

func (clock *authTestClock) advance(duration time.Duration) { clock.now = clock.now.Add(duration) }

type authTestIDs struct{ next int }

func (ids *authTestIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}

type fakeAuthDriver struct {
	startOutcome    Outcome
	startErr        error
	continueOutcome Outcome
	continueErr     error
	continueCalls   int
	lastInput       Input
}

func (driver *fakeAuthDriver) Start(context.Context, core.ProfileID) (Outcome, error) {
	return driver.startOutcome, driver.startErr
}

func (driver *fakeAuthDriver) Continue(_ context.Context, _ core.AuthSession, input Input) (Outcome, error) {
	driver.continueCalls++
	driver.lastInput = input
	return driver.continueOutcome, driver.continueErr
}

type fakeCredentialWriter struct {
	revision  uint64
	err       error
	calls     int
	reference string
	record    credentials.Record
}

func (writer *fakeCredentialWriter) Store(_ context.Context, reference string, record credentials.Record) (uint64, error) {
	writer.calls++
	writer.reference = reference
	writer.record = record
	if writer.err != nil {
		return 0, writer.err
	}
	if writer.revision == 0 {
		writer.revision = 1
	}
	return writer.revision, nil
}

func newAuthServiceFixture(t *testing.T, driver Driver, writer CredentialWriter) (*Service, *authTestClock, *storagememory.Repository) {
	t.Helper()
	repository := storagememory.NewRepository()
	clock := &authTestClock{now: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	service, err := NewService(repository, NewMemoryChallengeStore(), driver, writer, clock, &authTestIDs{})
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	return service, clock, repository
}

func startAuthSession(t *testing.T, service *Service) core.AuthSession {
	t.Helper()
	session, err := service.Start(context.Background(), StartRequest{
		Platform: "hh", ProfileID: "primary",
		CredentialReference: "file:/run/secrets/hh-primary.json",
	})
	if err != nil {
		t.Fatalf("start auth session: %v", err)
	}
	return session
}

func TestAuthServiceRunsCaptchaLoginAndDeletesPayload(t *testing.T) {
	ctx := context.Background()
	driver := &fakeAuthDriver{startOutcome: Outcome{Request: &Request{Identifier: true}}}
	writer := &fakeCredentialWriter{}
	service, _, repository := newAuthServiceFixture(t, driver, writer)
	session := startAuthSession(t, service)
	if session.Status != core.AuthSessionWaitingIdentifier {
		t.Fatalf("session = %#v", session)
	}
	driver.continueOutcome = Outcome{Request: &Request{Challenge: &Challenge{
		Kind: core.AuthChallengeCaptcha, MediaType: "image/png", Prompt: "Enter the code", Payload: []byte("png"),
	}}}
	session, err := service.Submit(ctx, session.ID, Input{Kind: InputIdentifier, Value: "user@example.com"})
	if err != nil {
		t.Fatalf("submit identifier: %v", err)
	}
	if session.Status != core.AuthSessionWaitingCaptcha || session.Challenge == nil {
		t.Fatalf("session = %#v", session)
	}
	payload, err := service.ChallengePayload(ctx, session.ID)
	if err != nil || string(payload.Data) != "png" {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
	driver.continueOutcome = Outcome{Credentials: &credentials.Record{AccessToken: "access", RefreshToken: "refresh"}}
	session, err = service.Submit(ctx, session.ID, Input{Kind: InputCaptcha, Value: "answer"})
	if err != nil {
		t.Fatalf("submit captcha: %v", err)
	}
	if session.Status != core.AuthSessionCompleted || session.CredentialReference != "file:/run/secrets/hh-primary.json" ||
		session.CredentialRevision != 1 || session.Challenge != nil {
		t.Fatalf("session = %#v", session)
	}
	if writer.calls != 1 || writer.reference != "file:/run/secrets/hh-primary.json" || writer.record.AccessToken != "access" {
		t.Fatalf("writer = %#v", writer)
	}
	if _, err := service.ChallengePayload(ctx, session.ID); !errors.Is(err, ErrChallengeNotFound) {
		t.Fatalf("payload after completion error = %v", err)
	}
	persisted, err := repository.AuthSession(ctx, session.ID)
	if err != nil || persisted.Status != core.AuthSessionCompleted || persisted.CredentialRevision != 1 {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
}

func TestAuthServiceFailsSessionOnDriverError(t *testing.T) {
	ctx := context.Background()
	driver := &fakeAuthDriver{startOutcome: Outcome{Request: &Request{Identifier: true}}}
	service, _, repository := newAuthServiceFixture(t, driver, &fakeCredentialWriter{})
	session := startAuthSession(t, service)
	driver.continueErr = &core.OperationError{
		Category: core.ErrorValidationRequired, Operation: "auth.otp", Message: "wrong code",
	}
	failed, err := service.Submit(ctx, session.ID, Input{Kind: InputIdentifier, Value: "user@example.com"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if failed.Status != core.AuthSessionFailed || failed.FailureCategory != core.ErrorValidationRequired {
		t.Fatalf("failed = %#v", failed)
	}
	persisted, err := repository.AuthSession(ctx, session.ID)
	if err != nil || persisted.Status != core.AuthSessionFailed {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
}

func TestAuthServiceExpiresAndSettlesSessions(t *testing.T) {
	ctx := context.Background()
	driver := &fakeAuthDriver{startOutcome: Outcome{Request: &Request{Identifier: true}}}
	service, clock, _ := newAuthServiceFixture(t, driver, &fakeCredentialWriter{})
	session := startAuthSession(t, service)
	if _, err := service.Submit(ctx, session.ID, Input{Kind: InputOTP, Value: "123456"}); !errors.Is(err, ErrAuthInputMismatch) {
		t.Fatalf("mismatched input error = %v", err)
	}
	clock.advance(DefaultSessionTTL + time.Second)
	expired, err := service.Submit(ctx, session.ID, Input{Kind: InputIdentifier, Value: "user@example.com"})
	if err != nil || expired.Status != core.AuthSessionExpired {
		t.Fatalf("expired=%#v err=%v", expired, err)
	}
	if _, err := service.Submit(ctx, session.ID, Input{Kind: InputIdentifier, Value: "user@example.com"}); !errors.Is(err, ErrAuthSessionSettled) {
		t.Fatalf("settled error = %v", err)
	}
}

func TestAuthServiceFailsWhenCredentialStorageFails(t *testing.T) {
	ctx := context.Background()
	driver := &fakeAuthDriver{startOutcome: Outcome{Request: &Request{Identifier: true}}}
	writer := &fakeCredentialWriter{err: errors.New("disk full")}
	service, _, _ := newAuthServiceFixture(t, driver, writer)
	session := startAuthSession(t, service)
	driver.continueOutcome = Outcome{Request: &Request{Challenge: &Challenge{
		Kind: core.AuthChallengeOTP, Prompt: "Enter the code",
	}}}
	session, err := service.Submit(ctx, session.ID, Input{Kind: InputIdentifier, Value: "user@example.com"})
	if err != nil {
		t.Fatalf("submit identifier: %v", err)
	}
	driver.continueOutcome = Outcome{Credentials: &credentials.Record{AccessToken: "access"}}
	failed, err := service.Submit(ctx, session.ID, Input{Kind: InputOTP, Value: "123456"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if failed.Status != core.AuthSessionFailed || failed.FailureCategory != core.ErrorPermanentFailure {
		t.Fatalf("failed = %#v", failed)
	}
}

func TestAuthServiceCancelDeletesPayload(t *testing.T) {
	ctx := context.Background()
	driver := &fakeAuthDriver{startOutcome: Outcome{Request: &Request{Identifier: true}}}
	service, _, _ := newAuthServiceFixture(t, driver, &fakeCredentialWriter{})
	session := startAuthSession(t, service)
	driver.continueOutcome = Outcome{Request: &Request{Challenge: &Challenge{
		Kind: core.AuthChallengeOTP, Prompt: "Enter the code",
	}}}
	session, err := service.Submit(ctx, session.ID, Input{Kind: InputIdentifier, Value: "user@example.com"})
	if err != nil {
		t.Fatalf("submit identifier: %v", err)
	}
	cancelled, err := service.Cancel(ctx, session.ID)
	if err != nil || cancelled.Status != core.AuthSessionCancelled {
		t.Fatalf("cancelled=%#v err=%v", cancelled, err)
	}
	if _, err := service.Cancel(ctx, session.ID); !errors.Is(err, ErrAuthSessionSettled) {
		t.Fatalf("second cancel error = %v", err)
	}
}

func TestFileCredentialWriterIncrementsRevisionAndRespectsOverwrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hh-primary.json")
	writer := &FileCredentialWriter{}
	revision, err := writer.Store(ctx, "file:"+path, credentials.Record{AccessToken: "first"})
	if err != nil || revision != 1 {
		t.Fatalf("first store revision=%d err=%v", revision, err)
	}
	if _, err := writer.Store(ctx, "file:"+path, credentials.Record{AccessToken: "second"}); !errors.Is(err, credentials.ErrSecretExists) {
		t.Fatalf("overwrite error = %v", err)
	}
	writer.Force = true
	revision, err = writer.Store(ctx, "file:"+path, credentials.Record{AccessToken: "second"})
	if err != nil || revision != 2 {
		t.Fatalf("second store revision=%d err=%v", revision, err)
	}
	loaded, err := credentials.Load("file:" + path)
	if err != nil || loaded.AccessToken != "second" || loaded.Revision != 2 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	dotenvPath := filepath.Join(t.TempDir(), "hh-primary.env")
	revision, err = writer.Store(ctx, "dotenv-file:"+dotenvPath, credentials.Record{AccessToken: "token"})
	if err != nil || revision != 1 {
		t.Fatalf("dotenv store revision=%d err=%v", revision, err)
	}
}
