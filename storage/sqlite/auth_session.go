package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

const authSessionColumns = `id, platform, profile_id, status, challenge, credential_reference,
	credential_revision, failure_category, failure_message, revision, expires_at, created_at, updated_at`

func (store *Store) CreateAuthSession(ctx context.Context, candidate core.AuthSession) (core.AuthSession, bool, error) {
	if err := candidate.Validate(); err != nil {
		return core.AuthSession{}, false, err
	}
	challenge, err := encodeAuthChallenge(candidate.Challenge)
	if err != nil {
		return core.AuthSession{}, false, err
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO auth_sessions (`+authSessionColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidate.ID, candidate.Platform, candidate.ProfileID, candidate.Status, challenge,
		candidate.CredentialReference, candidate.CredentialRevision, candidate.FailureCategory,
		candidate.FailureMessage, candidate.Revision, candidate.ExpiresAt.UnixNano(),
		candidate.CreatedAt.UnixNano(), candidate.UpdatedAt.UnixNano())
	if err != nil {
		return core.AuthSession{}, false, fmt.Errorf("insert auth session %s: %w", candidate.ID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil {
		return core.AuthSession{}, false, err
	}
	if created {
		return cloneSQLiteAuthSession(candidate), true, nil
	}
	stored, loadErr := store.AuthSession(ctx, candidate.ID)
	if loadErr != nil {
		return core.AuthSession{}, false, errors.New("auth session insert conflicted with an existing session")
	}
	if !sameSQLiteAuthSessionInputs(stored, candidate) {
		return core.AuthSession{}, false, errors.New("auth session id conflicts with different contents")
	}
	return stored, false, nil
}

func (store *Store) AuthSession(ctx context.Context, id core.AuthSessionID) (core.AuthSession, error) {
	if id == "" {
		return core.AuthSession{}, errors.New("auth session requires id")
	}
	session, err := scanAuthSession(store.db.QueryRowContext(ctx,
		`SELECT `+authSessionColumns+` FROM auth_sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return core.AuthSession{}, storage.ErrAuthSessionNotFound
	}
	return session, err
}

func (store *Store) SaveAuthSession(ctx context.Context, candidate core.AuthSession, expectedRevision uint64) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	stored, err := store.AuthSession(ctx, candidate.ID)
	if err != nil {
		return err
	}
	if candidate.Revision <= expectedRevision {
		return storage.ErrRevisionConflict
	}
	if !sameSQLiteAuthSessionInputs(stored, candidate) {
		return errors.New("auth session immutable inputs changed")
	}
	challenge, err := encodeAuthChallenge(candidate.Challenge)
	if err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `UPDATE auth_sessions SET
		status = ?, challenge = ?, credential_reference = ?, credential_revision = ?,
		failure_category = ?, failure_message = ?, revision = ?, updated_at = ?
		WHERE id = ? AND revision = ?`,
		candidate.Status, challenge, candidate.CredentialReference, candidate.CredentialRevision,
		candidate.FailureCategory, candidate.FailureMessage, candidate.Revision,
		candidate.UpdatedAt.UnixNano(), candidate.ID, expectedRevision)
	if err != nil {
		return fmt.Errorf("save auth session %s: %w", candidate.ID, err)
	}
	updated, err := oneRowAffected(result)
	if err != nil {
		return err
	}
	if !updated {
		return storage.ErrRevisionConflict
	}
	return nil
}

func scanAuthSession(row rowScanner) (core.AuthSession, error) {
	var session core.AuthSession
	var challengeJSON []byte
	var expiresAt, createdAt, updatedAt int64
	if err := row.Scan(
		&session.ID, &session.Platform, &session.ProfileID, &session.Status, &challengeJSON,
		&session.CredentialReference, &session.CredentialRevision, &session.FailureCategory,
		&session.FailureMessage, &session.Revision, &expiresAt, &createdAt, &updatedAt,
	); err != nil {
		return core.AuthSession{}, err
	}
	if len(challengeJSON) != 0 {
		var challenge core.AuthChallenge
		if err := json.Unmarshal(challengeJSON, &challenge); err != nil {
			return core.AuthSession{}, fmt.Errorf("decode auth challenge: %w", err)
		}
		session.Challenge = &challenge
	}
	session.ExpiresAt = time.Unix(0, expiresAt).UTC()
	session.CreatedAt = time.Unix(0, createdAt).UTC()
	session.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if err := session.Validate(); err != nil {
		return core.AuthSession{}, fmt.Errorf("invalid stored auth session %s: %w", session.ID, err)
	}
	return session, nil
}

func encodeAuthChallenge(challenge *core.AuthChallenge) (any, error) {
	if challenge == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(challenge)
	if err != nil {
		return nil, fmt.Errorf("encode auth challenge: %w", err)
	}
	return string(encoded), nil
}

func sameSQLiteAuthSessionInputs(left, right core.AuthSession) bool {
	return left.Platform == right.Platform && left.ProfileID == right.ProfileID &&
		left.CreatedAt.Equal(right.CreatedAt) && left.ExpiresAt.Equal(right.ExpiresAt)
}

func cloneSQLiteAuthSession(session core.AuthSession) core.AuthSession {
	if session.Challenge != nil {
		challenge := *session.Challenge
		session.Challenge = &challenge
	}
	return session
}
