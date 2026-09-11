package memory

import (
	"context"
	"errors"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func (repository *Repository) CreateAuthSession(ctx context.Context, candidate core.AuthSession) (core.AuthSession, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.AuthSession{}, false, err
	}
	if err := candidate.Validate(); err != nil {
		return core.AuthSession{}, false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if stored, exists := repository.authSessions[candidate.ID]; exists {
		if !sameAuthSessionInputs(stored, candidate) {
			return core.AuthSession{}, false, errors.New("auth session id conflicts with different contents")
		}
		return cloneAuthSession(stored), false, nil
	}
	repository.authSessions[candidate.ID] = cloneAuthSession(candidate)
	return cloneAuthSession(candidate), true, nil
}

func (repository *Repository) AuthSession(ctx context.Context, id core.AuthSessionID) (core.AuthSession, error) {
	if err := ctx.Err(); err != nil {
		return core.AuthSession{}, err
	}
	if id == "" {
		return core.AuthSession{}, errors.New("auth session requires id")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	session, exists := repository.authSessions[id]
	if !exists {
		return core.AuthSession{}, storage.ErrAuthSessionNotFound
	}
	return cloneAuthSession(session), nil
}

func (repository *Repository) SaveAuthSession(ctx context.Context, candidate core.AuthSession, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.authSessions[candidate.ID]
	if !exists {
		return storage.ErrAuthSessionNotFound
	}
	if stored.Revision != expectedRevision || candidate.Revision <= expectedRevision {
		return storage.ErrRevisionConflict
	}
	if !sameAuthSessionInputs(stored, candidate) {
		return errors.New("auth session immutable inputs changed")
	}
	repository.authSessions[candidate.ID] = cloneAuthSession(candidate)
	return nil
}

func sameAuthSessionInputs(left, right core.AuthSession) bool {
	return left.Platform == right.Platform && left.ProfileID == right.ProfileID && left.CreatedAt.Equal(right.CreatedAt) &&
		left.ExpiresAt.Equal(right.ExpiresAt)
}

func cloneAuthSession(session core.AuthSession) core.AuthSession {
	if session.Challenge != nil {
		challenge := *session.Challenge
		session.Challenge = &challenge
	}
	return session
}
