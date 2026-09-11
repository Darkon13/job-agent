package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/credentials"
	"github.com/Darkon13/job-agent/storage"
)

const (
	DefaultSessionTTL = 15 * time.Minute
	MaximumSessionTTL = time.Hour
)

var (
	// ErrAuthSessionSettled reports an operation on a completed, expired,
	// cancelled or failed session.
	ErrAuthSessionSettled = errors.New("auth session is already settled")
	// ErrAuthInputMismatch reports an input that does not match the current step.
	ErrAuthInputMismatch = errors.New("auth input does not match the current step")
)

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID(prefix string) (string, error)
}

type InputKind string

const (
	InputIdentifier InputKind = "identifier"
	InputOTP        InputKind = "otp"
	InputPassword   InputKind = "password"
	InputCaptcha    InputKind = "captcha"
)

type Input struct {
	Kind  InputKind
	Value string
}

// Challenge is one manual step requested by the driver. Payload stays in the
// short-lived store and is never written to the session record.
type Challenge struct {
	Kind      core.AuthChallengeKind
	MediaType string
	Prompt    string
	Payload   []byte
}

type Request struct {
	Identifier bool
	Challenge  *Challenge
}

// Outcome is either the next manual request or the exchanged artifacts the
// service must store. A login may produce OAuth credentials, browser storage
// state, or both.
type Outcome struct {
	Request      *Request
	Credentials  *credentials.Record
	BrowserState *BrowserState
}

// BrowserState is the sanitized Playwright storage state of a completed
// browser login. The driver owns platform sanitization; the writer only
// persists bytes atomically.
type BrowserState struct {
	Data json.RawMessage
}

type Driver interface {
	Start(ctx context.Context, profileID core.ProfileID) (Outcome, error)
	Continue(ctx context.Context, session core.AuthSession, input Input) (Outcome, error)
}

type CredentialWriter interface {
	Store(ctx context.Context, reference string, record credentials.Record) (uint64, error)
}

type BrowserStateWriter interface {
	Store(ctx context.Context, reference string, data json.RawMessage) (string, error)
}

type StartRequest struct {
	Platform              core.Platform
	ProfileID             core.ProfileID
	CredentialReference   string
	BrowserStateReference string
	TTL                   time.Duration
}

// Service drives one interactive login per call. A single process-local lock
// serializes sessions now; a per-profile lease replaces it with multi-replica
// runtime.
type Service struct {
	mu         sync.Mutex
	sessions   storage.AuthSessionRepository
	challenges ChallengeStore
	driver     Driver
	writer     CredentialWriter
	states     BrowserStateWriter
	clock      Clock
	ids        IDGenerator
}

func NewService(sessions storage.AuthSessionRepository, challenges ChallengeStore, driver Driver, writer CredentialWriter, states BrowserStateWriter, clock Clock, ids IDGenerator) (*Service, error) {
	if sessions == nil || challenges == nil || driver == nil || writer == nil || states == nil || clock == nil || ids == nil {
		return nil, errors.New("auth service requires sessions, challenges, driver, writers, clock and id generator")
	}
	return &Service{sessions: sessions, challenges: challenges, driver: driver, writer: writer, states: states, clock: clock, ids: ids}, nil
}

func (service *Service) Start(ctx context.Context, request StartRequest) (core.AuthSession, error) {
	if request.Platform == "" || request.ProfileID == "" {
		return core.AuthSession{}, errors.New("auth session requires platform and profile")
	}
	if strings.TrimSpace(request.CredentialReference) == "" && strings.TrimSpace(request.BrowserStateReference) == "" {
		return core.AuthSession{}, errors.New("auth session requires a credential or browser state reference")
	}
	ttl := request.TTL
	if ttl == 0 {
		ttl = DefaultSessionTTL
	}
	if ttl < 0 || ttl > MaximumSessionTTL {
		return core.AuthSession{}, errors.New("auth session ttl must be between zero and one hour")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.clock.Now().UTC()
	id, err := service.ids.NewID("auth-session")
	if err != nil {
		return core.AuthSession{}, err
	}
	session, err := core.NewAuthSession(core.NewAuthSessionParams{
		ID: core.AuthSessionID(id), Platform: request.Platform, ProfileID: request.ProfileID,
		CredentialReference:   request.CredentialReference,
		BrowserStateReference: request.BrowserStateReference,
		ExpiresAt:             now.Add(ttl),
	}, now)
	if err != nil {
		return core.AuthSession{}, err
	}
	outcome, err := service.driver.Start(ctx, request.ProfileID)
	if err != nil {
		return core.AuthSession{}, err
	}
	session, err = service.apply(ctx, session, outcome, now)
	if err != nil {
		return core.AuthSession{}, err
	}
	stored, _, err := service.sessions.CreateAuthSession(ctx, session)
	return stored, err
}

func (service *Service) Submit(ctx context.Context, id core.AuthSessionID, input Input) (core.AuthSession, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.clock.Now().UTC()
	session, err := service.sessions.AuthSession(ctx, id)
	if err != nil {
		return core.AuthSession{}, err
	}
	if session.IsTerminal() {
		return session, ErrAuthSessionSettled
	}
	if !now.Before(session.ExpiresAt) {
		expected := session.Revision
		if err := session.Expire(now); err != nil {
			return core.AuthSession{}, err
		}
		if err := service.sessions.SaveAuthSession(ctx, session, expected); err != nil {
			return core.AuthSession{}, err
		}
		return session, nil
	}
	if err := validateAuthInput(session, input); err != nil {
		return core.AuthSession{}, err
	}
	expected := session.Revision
	outcome, err := service.driver.Continue(ctx, session, input)
	if err != nil {
		if ctx.Err() != nil {
			return core.AuthSession{}, err
		}
		category, message, ok := authFailure(err)
		if !ok {
			return core.AuthSession{}, err
		}
		if failErr := session.Fail(category, message, now); failErr != nil {
			return core.AuthSession{}, failErr
		}
		if saveErr := service.sessions.SaveAuthSession(ctx, session, expected); saveErr != nil {
			return core.AuthSession{}, saveErr
		}
		return session, nil
	}
	session, err = service.apply(ctx, session, outcome, now)
	if err != nil {
		return core.AuthSession{}, err
	}
	if session.Revision != expected {
		if err := service.sessions.SaveAuthSession(ctx, session, expected); err != nil {
			return core.AuthSession{}, err
		}
	}
	return session, nil
}

func (service *Service) Cancel(ctx context.Context, id core.AuthSessionID) (core.AuthSession, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	session, err := service.sessions.AuthSession(ctx, id)
	if err != nil {
		return core.AuthSession{}, err
	}
	if session.IsTerminal() {
		return session, ErrAuthSessionSettled
	}
	challengeID := ""
	if session.Challenge != nil {
		challengeID = session.Challenge.ID
	}
	expected := session.Revision
	if err := session.Cancel(service.clock.Now().UTC()); err != nil {
		return core.AuthSession{}, err
	}
	if err := service.sessions.SaveAuthSession(ctx, session, expected); err != nil {
		return core.AuthSession{}, err
	}
	if challengeID != "" {
		_ = service.challenges.Delete(ctx, challengeID)
	}
	return session, nil
}

// ChallengePayload returns the ephemeral payload of the current step. It fails
// when the session has no challenge or the payload already expired.
func (service *Service) ChallengePayload(ctx context.Context, id core.AuthSessionID) (ChallengePayload, error) {
	session, err := service.sessions.AuthSession(ctx, id)
	if err != nil {
		return ChallengePayload{}, err
	}
	if session.Challenge == nil {
		return ChallengePayload{}, ErrChallengeNotFound
	}
	return service.challenges.Get(ctx, session.Challenge.ID, service.clock.Now().UTC())
}

// Session returns the durable, redacted session record.
func (service *Service) Session(ctx context.Context, id core.AuthSessionID) (core.AuthSession, error) {
	return service.sessions.AuthSession(ctx, id)
}

func (service *Service) apply(ctx context.Context, session core.AuthSession, outcome Outcome, now time.Time) (core.AuthSession, error) {
	switch {
	case outcome.Credentials != nil || outcome.BrowserState != nil:
		return service.store(ctx, session, outcome, now)
	case outcome.Request == nil:
		return core.AuthSession{}, errors.New("auth driver returned an empty outcome")
	case outcome.Request.Identifier:
		if session.Status == core.AuthSessionWaitingIdentifier {
			return session, nil
		}
		if err := session.BeginIdentifier(now); err != nil {
			return core.AuthSession{}, err
		}
		return session, nil
	default:
		challenge := outcome.Request.Challenge
		if challenge == nil {
			return core.AuthSession{}, errors.New("auth driver request requires an identifier or challenge")
		}
		previous := ""
		if session.Challenge != nil {
			previous = session.Challenge.ID
		}
		id, err := service.ids.NewID("auth-challenge")
		if err != nil {
			return core.AuthSession{}, err
		}
		descriptor := core.AuthChallenge{
			ID: id, Kind: challenge.Kind, MediaType: challenge.MediaType,
			Prompt: challenge.Prompt, Deadline: now.Add(DefaultChallengeTTL),
		}
		if len(challenge.Payload) != 0 {
			payload := ChallengePayload{MediaType: challenge.MediaType, Data: challenge.Payload}
			if err := service.challenges.Put(ctx, id, payload, DefaultChallengeTTL, now); err != nil {
				return core.AuthSession{}, err
			}
		}
		if err := session.RequireChallenge(descriptor, now); err != nil {
			return core.AuthSession{}, err
		}
		if previous != "" && previous != id {
			_ = service.challenges.Delete(ctx, previous)
		}
		return session, nil
	}
}

func (service *Service) store(ctx context.Context, session core.AuthSession, outcome Outcome, now time.Time) (core.AuthSession, error) {
	challengeID := ""
	if session.Challenge != nil {
		challengeID = session.Challenge.ID
	}
	if session.WaitingChallenge() || session.Status == core.AuthSessionWaitingIdentifier {
		if err := session.BeginExchange(now); err != nil {
			return core.AuthSession{}, err
		}
	}
	if challengeID != "" {
		_ = service.challenges.Delete(ctx, challengeID)
	}
	if err := session.BeginStoring(now); err != nil {
		return core.AuthSession{}, err
	}
	var credentialRevision uint64
	if outcome.Credentials != nil {
		revision, err := service.writer.Store(ctx, session.CredentialReference, *outcome.Credentials)
		if err != nil {
			return service.failStorage(ctx, session, now)
		}
		credentialRevision = revision
	}
	var browserStateDigest string
	if outcome.BrowserState != nil {
		digest, err := service.states.Store(ctx, session.BrowserStateReference, outcome.BrowserState.Data)
		if err != nil {
			return service.failStorage(ctx, session, now)
		}
		browserStateDigest = digest
	}
	if err := session.Complete(credentialRevision, browserStateDigest, now); err != nil {
		return core.AuthSession{}, err
	}
	return session, nil
}

// failStorage settles the session because the exchanged one-time artifacts
// cannot be replayed by a retry.
func (service *Service) failStorage(_ context.Context, session core.AuthSession, now time.Time) (core.AuthSession, error) {
	if err := session.Fail(core.ErrorPermanentFailure, "artifact storage failed after a successful exchange", now); err != nil {
		return core.AuthSession{}, err
	}
	return session, nil
}

func validateAuthInput(session core.AuthSession, input Input) error {
	switch {
	case session.Status == core.AuthSessionWaitingIdentifier:
		if input.Kind != InputIdentifier {
			return fmt.Errorf("%w: identifier expected", ErrAuthInputMismatch)
		}
	case session.WaitingChallenge():
		if session.Challenge == nil || string(session.Challenge.Kind) != string(input.Kind) {
			return fmt.Errorf("%w: %s expected", ErrAuthInputMismatch, session.Challenge.Kind)
		}
	default:
		return fmt.Errorf("%w: session is not waiting for input", ErrAuthInputMismatch)
	}
	if !utf8.ValidString(input.Value) || strings.TrimSpace(input.Value) == "" || utf8.RuneCountInString(input.Value) > 4096 {
		return errors.New("auth input requires a bounded non-empty value")
	}
	return nil
}

func authFailure(err error) (core.ErrorCategory, string, bool) {
	var operationError *core.OperationError
	if !errors.As(err, &operationError) || operationError.Validate() != nil {
		return "", "", false
	}
	message := strings.TrimSpace(operationError.Message)
	if message == "" {
		message = string(operationError.Category)
	}
	if utf8.RuneCountInString(message) > 512 {
		runes := []rune(message)
		message = string(runes[:512])
	}
	return operationError.Category, message, true
}

// FileCredentialWriter stores records through the credentials package and
// increments the revision of an existing record. Force allows replacing an
// existing secret and is an explicit CLI decision.
type FileCredentialWriter struct {
	Force bool
}

func (writer *FileCredentialWriter) Store(_ context.Context, reference string, record credentials.Record) (uint64, error) {
	parsed, err := credentials.ParseReference(reference)
	if err != nil {
		return 0, err
	}
	format := credentials.FormatJSON
	if parsed.Scheme == "dotenv-file" {
		format = credentials.FormatDotenv
	}
	revision := uint64(1)
	if existing, loadErr := credentials.Load(reference); loadErr == nil {
		revision = existing.Revision + 1
	}
	record.Revision = revision
	if err := credentials.WriteFile(parsed.Path, record, format, writer.Force); err != nil {
		return 0, err
	}
	return revision, nil
}

// FileBrowserStateWriter persists sanitized Playwright storage state to the
// configured path atomically with 0600 permissions and returns its digest.
type FileBrowserStateWriter struct{}

func (writer *FileBrowserStateWriter) Store(_ context.Context, reference string, data json.RawMessage) (string, error) {
	path := strings.TrimSpace(reference)
	path = strings.TrimPrefix(path, "file:")
	if path == "" || strings.Contains(path, "://") {
		return "", errors.New("browser state reference must be a file path")
	}
	if !json.Valid(data) {
		return "", errors.New("browser state must be valid JSON")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("browser state output must not be a symlink")
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create browser state output: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", fmt.Errorf("protect browser state output: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return "", fmt.Errorf("write browser state output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("flush browser state output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close browser state output: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return "", fmt.Errorf("replace browser state output: %w", err)
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
