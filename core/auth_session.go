package core

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type AuthSessionID string

type AuthSessionStatus string

const (
	AuthSessionCreated           AuthSessionStatus = "created"
	AuthSessionWaitingIdentifier AuthSessionStatus = "waiting_identifier"
	AuthSessionWaitingOTP        AuthSessionStatus = "waiting_otp"
	AuthSessionWaitingPassword   AuthSessionStatus = "waiting_password"
	AuthSessionWaitingCaptcha    AuthSessionStatus = "waiting_captcha"
	AuthSessionExchanging        AuthSessionStatus = "exchanging"
	AuthSessionStoring           AuthSessionStatus = "storing"
	AuthSessionCompleted         AuthSessionStatus = "completed"
	AuthSessionExpired           AuthSessionStatus = "expired"
	AuthSessionCancelled         AuthSessionStatus = "cancelled"
	AuthSessionFailed            AuthSessionStatus = "failed"
)

type AuthChallengeKind string

const (
	AuthChallengeOTP      AuthChallengeKind = "otp"
	AuthChallengePassword AuthChallengeKind = "password"
	AuthChallengeCaptcha  AuthChallengeKind = "captcha"
)

const (
	maximumAuthChallengePromptRunes = 512
	maximumAuthFailureMessageRunes  = 512
)

// AuthChallenge is the redacted descriptor of the current manual step. The
// captcha image, OTP text and entered answer live only in the short-lived
// challenge store, never in the persistent session.
type AuthChallenge struct {
	ID        string            `json:"id"`
	Kind      AuthChallengeKind `json:"kind"`
	MediaType string            `json:"media_type,omitempty"`
	Prompt    string            `json:"prompt,omitempty"`
	Deadline  time.Time         `json:"deadline"`
}

func (challenge AuthChallenge) Validate(now time.Time) error {
	if strings.TrimSpace(challenge.ID) == "" {
		return errors.New("auth challenge requires id")
	}
	switch challenge.Kind {
	case AuthChallengeOTP, AuthChallengePassword, AuthChallengeCaptcha:
	default:
		return fmt.Errorf("unknown auth challenge kind %q", challenge.Kind)
	}
	if len(challenge.MediaType) > 64 {
		return errors.New("auth challenge media type is too long")
	}
	if err := validateAuthText(challenge.Prompt, maximumAuthChallengePromptRunes, "auth challenge prompt"); err != nil {
		return err
	}
	if challenge.Deadline.IsZero() || (now != time.Time{} && !challenge.Deadline.After(now)) {
		return errors.New("auth challenge requires a future deadline")
	}
	return nil
}

// AuthSession owns one interactive login. It stores only redacted metadata:
// identifiers, codes, tokens and images never enter the session record.
type AuthSession struct {
	ID                    AuthSessionID     `json:"id"`
	Platform              Platform          `json:"platform"`
	ProfileID             ProfileID         `json:"profile_id"`
	Status                AuthSessionStatus `json:"status"`
	Challenge             *AuthChallenge    `json:"challenge,omitempty"`
	CredentialReference   string            `json:"credential_reference,omitempty"`
	CredentialRevision    uint64            `json:"credential_revision,omitempty"`
	BrowserStateReference string            `json:"browser_state_reference,omitempty"`
	BrowserStateDigest    string            `json:"browser_state_digest,omitempty"`
	FailureCategory       ErrorCategory     `json:"failure_category,omitempty"`
	FailureMessage        string            `json:"failure_message,omitempty"`
	Revision              uint64            `json:"revision"`
	ExpiresAt             time.Time         `json:"expires_at"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
}

type NewAuthSessionParams struct {
	ID                    AuthSessionID
	Platform              Platform
	ProfileID             ProfileID
	CredentialReference   string
	BrowserStateReference string
	ExpiresAt             time.Time
}

func NewAuthSession(params NewAuthSessionParams, now time.Time) (AuthSession, error) {
	session := AuthSession{
		ID: params.ID, Platform: params.Platform, ProfileID: params.ProfileID,
		CredentialReference:   strings.TrimSpace(params.CredentialReference),
		BrowserStateReference: strings.TrimSpace(params.BrowserStateReference),
		Status:                AuthSessionCreated, Revision: 1, ExpiresAt: params.ExpiresAt.UTC(),
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := session.Validate(); err != nil {
		return AuthSession{}, err
	}
	if !session.ExpiresAt.After(session.CreatedAt) {
		return AuthSession{}, errors.New("auth session requires an expiry after creation")
	}
	return session, nil
}

func (session AuthSession) Validate() error {
	if session.ID == "" || session.Platform == "" || session.ProfileID == "" {
		return errors.New("auth session requires id, platform and profile")
	}
	if session.Revision == 0 || session.CreatedAt.IsZero() || session.UpdatedAt.IsZero() ||
		session.UpdatedAt.Before(session.CreatedAt) || session.ExpiresAt.IsZero() {
		return errors.New("auth session requires revision and valid timestamps")
	}
	// References are declared destinations and may be set before the artifacts
	// are stored; revisions and digests appear only on completion.
	if session.Status != AuthSessionCompleted && (session.CredentialRevision != 0 || session.BrowserStateDigest != "") {
		return errors.New("auth session artifact revisions are only valid for a completed session")
	}
	switch session.Status {
	case AuthSessionCreated, AuthSessionWaitingIdentifier, AuthSessionExchanging, AuthSessionStoring:
		if session.Challenge != nil {
			return fmt.Errorf("auth session in status %q cannot keep a challenge", session.Status)
		}
		if session.FailureCategory != "" {
			return fmt.Errorf("auth session in status %q cannot contain a failure", session.Status)
		}
	case AuthSessionWaitingOTP, AuthSessionWaitingPassword, AuthSessionWaitingCaptcha:
		if session.Challenge == nil {
			return fmt.Errorf("auth session in status %q requires a challenge", session.Status)
		}
		if err := session.Challenge.Validate(time.Time{}); err != nil {
			return err
		}
		if !challengeKindMatchesStatus(session.Challenge.Kind, session.Status) {
			return errors.New("auth challenge kind does not match session status")
		}
		if session.FailureCategory != "" {
			return fmt.Errorf("auth session in status %q cannot contain a failure", session.Status)
		}
	case AuthSessionCompleted:
		if session.Challenge != nil {
			return errors.New("completed auth session cannot keep a challenge")
		}
		credentialStored := session.CredentialReference != "" && session.CredentialRevision > 0
		stateStored := session.BrowserStateReference != "" && validApplicationDigest(session.BrowserStateDigest)
		if !credentialStored && !stateStored {
			return errors.New("completed auth session requires a stored credential or browser state")
		}
		if session.CredentialReference == "" && session.CredentialRevision != 0 {
			return errors.New("auth session credential revision requires a credential reference")
		}
		if session.BrowserStateReference == "" && session.BrowserStateDigest != "" {
			return errors.New("auth session browser state digest requires a state reference")
		}
		if session.FailureCategory != "" {
			return errors.New("completed auth session cannot contain a failure")
		}
	case AuthSessionExpired, AuthSessionCancelled:
		if session.Challenge != nil || session.FailureCategory != "" {
			return fmt.Errorf("auth session in status %q cannot contain a result", session.Status)
		}
	case AuthSessionFailed:
		if session.Challenge != nil {
			return errors.New("failed auth session cannot contain a challenge")
		}
		if !ValidErrorCategory(session.FailureCategory) {
			return errors.New("failed auth session requires a known failure category")
		}
	default:
		return fmt.Errorf("unknown auth session status %q", session.Status)
	}
	if session.Status != AuthSessionFailed && session.FailureCategory != "" {
		return errors.New("only failed auth sessions may contain a failure category")
	}
	if session.Status != AuthSessionFailed && session.FailureMessage != "" {
		return errors.New("only failed auth sessions may contain a failure message")
	}
	if err := validateAuthText(session.FailureMessage, maximumAuthFailureMessageRunes, "auth failure message"); err != nil {
		return err
	}
	return nil
}

func (session AuthSession) IsTerminal() bool {
	switch session.Status {
	case AuthSessionCompleted, AuthSessionExpired, AuthSessionCancelled, AuthSessionFailed:
		return true
	default:
		return false
	}
}

func (session AuthSession) WaitingChallenge() bool {
	switch session.Status {
	case AuthSessionWaitingOTP, AuthSessionWaitingPassword, AuthSessionWaitingCaptcha:
		return true
	default:
		return false
	}
}

func (session *AuthSession) BeginIdentifier(now time.Time) error {
	if session == nil || session.Status != AuthSessionCreated {
		return errors.New("only a created auth session can request an identifier")
	}
	if err := session.advance(now); err != nil {
		return err
	}
	session.Status = AuthSessionWaitingIdentifier
	return session.Validate()
}

func (session *AuthSession) RequireChallenge(challenge AuthChallenge, now time.Time) error {
	if session == nil {
		return errors.New("auth session is nil")
	}
	switch session.Status {
	case AuthSessionWaitingIdentifier, AuthSessionWaitingOTP, AuthSessionWaitingPassword, AuthSessionWaitingCaptcha, AuthSessionExchanging:
	default:
		return fmt.Errorf("cannot require a challenge in status %q", session.statusOrEmpty())
	}
	if err := challenge.Validate(now); err != nil {
		return err
	}
	if challenge.Deadline.After(session.ExpiresAt) {
		return errors.New("auth challenge deadline must not outlive the session")
	}
	if session.Challenge != nil && session.Challenge.ID == challenge.ID && session.Challenge.Kind == challenge.Kind {
		return nil
	}
	if err := session.advance(now); err != nil {
		return err
	}
	session.Challenge = &challenge
	session.Status = authStatusForChallengeKind(challenge.Kind)
	return session.Validate()
}

func (session *AuthSession) BeginExchange(now time.Time) error {
	if session == nil {
		return errors.New("auth session is nil")
	}
	if !session.WaitingChallenge() && session.Status != AuthSessionWaitingIdentifier {
		return fmt.Errorf("cannot begin exchange in status %q", session.statusOrEmpty())
	}
	if err := session.advance(now); err != nil {
		return err
	}
	session.Challenge = nil
	session.Status = AuthSessionExchanging
	return session.Validate()
}

func (session *AuthSession) BeginStoring(now time.Time) error {
	if session == nil || session.Status != AuthSessionExchanging {
		return fmt.Errorf("cannot begin storing in status %q", session.statusOrEmpty())
	}
	if strings.TrimSpace(session.CredentialReference) == "" && strings.TrimSpace(session.BrowserStateReference) == "" {
		return errors.New("auth session storing requires a credential or browser state reference")
	}
	if err := session.advance(now); err != nil {
		return err
	}
	session.Status = AuthSessionStoring
	return session.Validate()
}

// Complete finishes the session after at least one artifact was stored. The
// caller passes zero values for artifacts that do not apply to this login.
func (session *AuthSession) Complete(credentialRevision uint64, browserStateDigest string, now time.Time) error {
	if session == nil || session.Status != AuthSessionStoring {
		return fmt.Errorf("cannot complete auth session in status %q", session.statusOrEmpty())
	}
	browserStateDigest = strings.TrimSpace(browserStateDigest)
	credentialStored := session.CredentialReference != "" && credentialRevision > 0
	stateStored := session.BrowserStateReference != "" && browserStateDigest != ""
	if !credentialStored && !stateStored {
		return errors.New("completed auth session requires a stored credential or browser state")
	}
	if session.CredentialReference == "" && credentialRevision != 0 {
		return errors.New("auth session credential revision requires a credential reference")
	}
	if session.BrowserStateReference == "" && browserStateDigest != "" {
		return errors.New("auth session browser state digest requires a state reference")
	}
	if browserStateDigest != "" && !validApplicationDigest(browserStateDigest) {
		return errors.New("auth session browser state digest must be sha256:<hex>")
	}
	if err := session.advance(now); err != nil {
		return err
	}
	session.CredentialRevision = credentialRevision
	session.BrowserStateDigest = browserStateDigest
	session.Status = AuthSessionCompleted
	return session.Validate()
}

func (session *AuthSession) Fail(category ErrorCategory, message string, now time.Time) error {
	if session == nil || session.IsTerminal() {
		return fmt.Errorf("cannot fail auth session in status %q", session.statusOrEmpty())
	}
	if !ValidErrorCategory(category) {
		return fmt.Errorf("unknown error category %q", category)
	}
	if err := validateAuthText(message, maximumAuthFailureMessageRunes, "auth failure message"); err != nil {
		return err
	}
	if err := session.advance(now); err != nil {
		return err
	}
	session.Challenge = nil
	session.Status = AuthSessionFailed
	session.FailureCategory = category
	session.FailureMessage = message
	return session.Validate()
}

func (session *AuthSession) Cancel(now time.Time) error {
	if session == nil || session.IsTerminal() {
		return fmt.Errorf("cannot cancel auth session in status %q", session.statusOrEmpty())
	}
	if err := session.advance(now); err != nil {
		return err
	}
	session.Challenge = nil
	session.Status = AuthSessionCancelled
	return session.Validate()
}

func (session *AuthSession) Expire(now time.Time) error {
	if session == nil || session.IsTerminal() {
		return fmt.Errorf("cannot expire auth session in status %q", session.statusOrEmpty())
	}
	if now.Before(session.ExpiresAt) {
		return errors.New("auth session expiry has not been reached")
	}
	if err := session.advance(now); err != nil {
		return err
	}
	session.Challenge = nil
	session.Status = AuthSessionExpired
	return session.Validate()
}

func (session AuthSession) statusOrEmpty() AuthSessionStatus {
	if session.Status == "" {
		return AuthSessionStatus("unknown")
	}
	return session.Status
}

func (session *AuthSession) advance(now time.Time) error {
	if now.IsZero() || now.Before(session.UpdatedAt) {
		return errors.New("auth session update time must not move backwards")
	}
	session.Revision++
	session.UpdatedAt = now.UTC()
	return nil
}

func authStatusForChallengeKind(kind AuthChallengeKind) AuthSessionStatus {
	switch kind {
	case AuthChallengeOTP:
		return AuthSessionWaitingOTP
	case AuthChallengePassword:
		return AuthSessionWaitingPassword
	default:
		return AuthSessionWaitingCaptcha
	}
}

func challengeKindFromStatus(status AuthSessionStatus) AuthChallengeKind {
	switch status {
	case AuthSessionWaitingOTP:
		return AuthChallengeOTP
	case AuthSessionWaitingPassword:
		return AuthChallengePassword
	case AuthSessionWaitingCaptcha:
		return AuthChallengeCaptcha
	default:
		return ""
	}
}

func challengeKindMatchesStatus(kind AuthChallengeKind, status AuthSessionStatus) bool {
	return challengeKindFromStatus(status) == kind
}

func validateAuthText(value string, maximumRunes int, label string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", label)
	}
	if utf8.RuneCountInString(value) > maximumRunes {
		return fmt.Errorf("%s is too long", label)
	}
	for _, symbol := range value {
		if unicode.IsControl(symbol) && symbol != '\n' && symbol != '\t' {
			return fmt.Errorf("%s contains an unsupported control character", label)
		}
	}
	return nil
}
