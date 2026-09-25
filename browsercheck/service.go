// Package browsercheck owns interactive applications that need an operator:
// the platform stopped an automated submission and the same action can be
// completed from the profile's real browser session. A check starts a browser
// flow, keeps the optional captcha image in memory for a short time, accepts
// the operator's answer, and confirms the application through the ordinary
// retry pipeline once the platform accepted it.
package browsercheck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/core"
)

const (
	// DefaultSessionTTL bounds how long an unfinished check keeps its payload.
	DefaultSessionTTL = 15 * time.Minute
	// MaximumSessionTTL is the largest accepted session lifetime.
	MaximumSessionTTL = 30 * time.Minute
)

// State is the observable progress of one interactive check.
type State string

const (
	// StateWaitingCaptcha means the platform shows a captcha and waits for an answer.
	StateWaitingCaptcha State = "waiting_captcha"
	// StateDone means the browser completed the submission; the retry pipeline confirms it.
	StateDone State = "done"
	// StateReview means the browser flow stopped in a state an operator must inspect.
	StateReview State = "review"
)

var (
	// ErrSessionNotFound reports an unknown, expired or cancelled session.
	ErrSessionNotFound = errors.New("browser check session not found")
	// ErrSessionBusy reports a concurrent start for the same application.
	ErrSessionBusy = errors.New("browser check is already running for this application")
	// ErrSessionState reports an answer sent to a session that does not wait for one.
	ErrSessionState = errors.New("browser check session does not accept an answer")
	// ErrSessionInvalid reports an empty session or answer value.
	ErrSessionInvalid = errors.New("browser check session is invalid")
)

// Outcome is what one browser step produced.
type Outcome struct {
	State   State
	Image   []byte
	Message string
}

// Driver performs the platform-specific browser interaction. Implementations
// must keep the profile's page open between Submit and Answer.
type Driver interface {
	// Submit opens the application page and tries the ordinary apply flow.
	// A non-empty letter is offered to the platform when it asks for one.
	Submit(ctx context.Context, profileID core.ProfileID, vacancyID string, letter string) (Outcome, error)
	// Answer delivers the captcha answer to the still-open page. A non-empty
	// letter is offered when the platform asks for a cover letter next.
	Answer(ctx context.Context, profileID core.ProfileID, answer string, letter string) (Outcome, error)
}

// ApplicationReader loads the application selected for a check.
type ApplicationReader interface {
	ApplicationByID(ctx context.Context, id core.ApplicationID) (core.Application, error)
}

// RetryEnqueuer confirms a browser-submitted application through the durable
// submit workflow instead of writing application state directly.
type RetryEnqueuer interface {
	Enqueue(ctx context.Context, applicationID core.ApplicationID, requestKey string) (core.Task, bool, error)
}

// Clock supplies the current time.
type Clock interface {
	Now() time.Time
}

// IDGenerator produces session identifiers.
type IDGenerator interface {
	NewID(prefix string) (string, error)
}

// Session is the dashboard and CLI facing state of one check.
type Session struct {
	ID            string             `json:"session_id"`
	ApplicationID core.ApplicationID `json:"application_id"`
	ProfileID     core.ProfileID     `json:"profile_id,omitempty"`
	State         State              `json:"state"`
	Message       string             `json:"message,omitempty"`
	HasImage      bool               `json:"has_image"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

type sessionEntry struct {
	session Session
	image   []byte
	expires time.Time
}

// Service tracks one active check per application.
type Service struct {
	driver       Driver
	applications ApplicationReader
	retry        RetryEnqueuer
	clock        Clock
	ids          IDGenerator
	ttl          time.Duration

	mu       sync.Mutex
	sessions map[string]*sessionEntry
	busy     map[core.ApplicationID]struct{}
}

// NewService validates the dependencies of the interactive check service.
func NewService(driver Driver, applications ApplicationReader, retry RetryEnqueuer, clock Clock, ids IDGenerator) (*Service, error) {
	if driver == nil || applications == nil || retry == nil || clock == nil || ids == nil {
		return nil, errors.New("browser check service requires driver, applications, retry, clock and ids")
	}
	return &Service{
		driver: driver, applications: applications, retry: retry, clock: clock, ids: ids,
		ttl:      DefaultSessionTTL,
		sessions: make(map[string]*sessionEntry),
		busy:     make(map[core.ApplicationID]struct{}),
	}, nil
}

// ConfigureTTL overrides the session lifetime for tests and tuning.
func (service *Service) ConfigureTTL(ttl time.Duration) error {
	if ttl <= 0 || ttl > MaximumSessionTTL {
		return fmt.Errorf("browser check ttl must be between 1s and %s", MaximumSessionTTL)
	}
	service.ttl = ttl
	return nil
}

// Start begins a check for one application. A live session for the same
// application is returned instead of starting a second browser flow.
func (service *Service) Start(ctx context.Context, applicationID core.ApplicationID) (Session, error) {
	if service == nil {
		return Session{}, errors.New("browser check service is nil")
	}
	applicationID = core.ApplicationID(strings.TrimSpace(string(applicationID)))
	if applicationID == "" {
		return Session{}, ErrSessionInvalid
	}
	now := service.clock.Now()
	if session, ok := service.liveSession(applicationID, now); ok {
		return session, nil
	}
	application, err := service.applications.ApplicationByID(ctx, applicationID)
	if err != nil {
		return Session{}, err
	}
	if err := service.claim(applicationID); err != nil {
		return Session{}, err
	}
	defer service.release(applicationID)

	outcome, err := service.driver.Submit(ctx, application.Key.ProfileID, application.Key.Vacancy.ExternalID, strings.TrimSpace(application.PreparedMessage))
	if err != nil {
		return Session{}, err
	}
	return service.store(ctx, application, outcome)
}

// Answer delivers the operator's captcha answer and stores the next outcome.
func (service *Service) Answer(ctx context.Context, applicationID core.ApplicationID, sessionID string, value string) (Session, error) {
	if service == nil {
		return Session{}, errors.New("browser check service is nil")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return Session{}, ErrSessionInvalid
	}
	entry, err := service.entry(applicationID, sessionID)
	if err != nil {
		return Session{}, err
	}
	if entry.session.State != StateWaitingCaptcha {
		return Session{}, ErrSessionState
	}
	if err := service.claim(applicationID); err != nil {
		return Session{}, err
	}
	defer service.release(applicationID)

	application, err := service.applications.ApplicationByID(ctx, entry.session.ApplicationID)
	if err != nil {
		return Session{}, err
	}
	outcome, err := service.driver.Answer(ctx, entry.session.ProfileID, value, strings.TrimSpace(application.PreparedMessage))
	if err != nil {
		return Session{}, err
	}
	return service.store(ctx, application, outcome)
}

// Status returns the stored session without touching the browser.
func (service *Service) Status(applicationID core.ApplicationID, sessionID string) (Session, error) {
	if service == nil {
		return Session{}, errors.New("browser check service is nil")
	}
	entry, err := service.entry(applicationID, sessionID)
	if err != nil {
		return Session{}, err
	}
	return entry.session, nil
}

// Image returns the captcha or review screenshot of one session.
func (service *Service) Image(ctx context.Context, applicationID core.ApplicationID, sessionID string) ([]byte, error) {
	entry, err := service.entry(applicationID, sessionID)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), entry.image...), nil
}

// Cancel forgets a session. The browser page stays as it is; the next check
// reloads the vacancy page anyway.
func (service *Service) Cancel(applicationID core.ApplicationID, sessionID string) error {
	if service == nil {
		return errors.New("browser check service is nil")
	}
	entry, err := service.entry(applicationID, sessionID)
	if err != nil {
		return err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	delete(service.sessions, entry.session.ID)
	return nil
}

// store persists one outcome and confirms a finished submission through the
// retry workflow so the ordinary pipeline records the platform result.
func (service *Service) store(ctx context.Context, application core.Application, outcome Outcome) (Session, error) {
	if outcome.State == "" {
		outcome.State = StateReview
	}
	now := service.clock.Now()
	entry := &sessionEntry{
		session: Session{
			ApplicationID: application.ID,
			ProfileID:     application.Key.ProfileID,
			State:         outcome.State,
			Message:       strings.TrimSpace(outcome.Message),
			HasImage:      len(outcome.Image) != 0,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		image:   append([]byte(nil), outcome.Image...),
		expires: now.Add(service.ttl),
	}
	if outcome.State == StateDone {
		confirmation, err := service.confirm(ctx, application, now)
		if err != nil {
			return Session{}, err
		}
		entry.session.Message = joinMessage(entry.session.Message, confirmation)
	}
	id, err := service.ids.NewID("browsercheck")
	if err != nil {
		return Session{}, err
	}
	entry.session.ID = id
	service.mu.Lock()
	defer service.mu.Unlock()
	service.sessions[id] = entry
	return entry.session, nil
}

// confirm routes a browser-submitted application through the durable retry
// workflow. An application that is already confirmed needs no second task.
func (service *Service) confirm(ctx context.Context, application core.Application, now time.Time) (string, error) {
	if application.Status == core.ApplicationSubmitted {
		return "отклик уже подтверждён", nil
	}
	task, _, err := service.retry.Enqueue(ctx, application.ID, "browser-check-"+now.UTC().Format("20060102150405"))
	if err != nil {
		current, lookupErr := service.applications.ApplicationByID(ctx, application.ID)
		if lookupErr == nil && current.Status == core.ApplicationSubmitted {
			return "отклик уже подтверждён", nil
		}
		return "", fmt.Errorf("confirm browser submission: %w", err)
	}
	return "отклик отправлен из браузера, подтверждение задачей " + string(task.ID), nil
}

func joinMessage(prefix, suffix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return suffix
	}
	return prefix + "; " + suffix
}

func (service *Service) claim(applicationID core.ApplicationID) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if _, exists := service.busy[applicationID]; exists {
		return ErrSessionBusy
	}
	service.busy[applicationID] = struct{}{}
	return nil
}

func (service *Service) release(applicationID core.ApplicationID) {
	service.mu.Lock()
	defer service.mu.Unlock()
	delete(service.busy, applicationID)
}

func (service *Service) liveSession(applicationID core.ApplicationID, now time.Time) (Session, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	for id, entry := range service.sessions {
		if entry.session.ApplicationID != applicationID {
			continue
		}
		if !now.Before(entry.expires) {
			delete(service.sessions, id)
			continue
		}
		return entry.session, true
	}
	return Session{}, false
}

func (service *Service) entry(applicationID core.ApplicationID, sessionID string) (*sessionEntry, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, ErrSessionInvalid
	}
	now := service.clock.Now()
	service.mu.Lock()
	defer service.mu.Unlock()
	entry, exists := service.sessions[sessionID]
	if !exists || entry.session.ApplicationID != applicationID || !now.Before(entry.expires) {
		if exists && !now.Before(entry.expires) {
			delete(service.sessions, sessionID)
		}
		return nil, ErrSessionNotFound
	}
	return entry, nil
}
