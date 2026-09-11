package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var _ storage.TestAttemptRepository = (*Store)(nil)

func (store *Store) SaveTestAttempt(ctx context.Context, attempt core.TestAttempt) error {
	if err := attempt.Validate(); err != nil {
		return err
	}
	_, err := store.db.ExecContext(ctx, `INSERT INTO test_attempts
		(platform, profile_id, external_id, test_definition_id, status, attempt_fingerprint, attempts, observed_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform, profile_id, external_id) DO UPDATE SET
			test_definition_id = excluded.test_definition_id,
			status = CASE WHEN test_attempts.status = 'passed' THEN test_attempts.status ELSE excluded.status END,
			attempt_fingerprint = CASE WHEN test_attempts.status = 'passed' THEN test_attempts.attempt_fingerprint ELSE excluded.attempt_fingerprint END,
			attempts = MAX(test_attempts.attempts, excluded.attempts),
			observed_at = CASE WHEN test_attempts.status = 'passed' THEN test_attempts.observed_at ELSE excluded.observed_at END,
			updated_at = MAX(test_attempts.updated_at, excluded.updated_at)`,
		attempt.Platform, attempt.ProfileID, attempt.ExternalID, attempt.TestDefinitionID,
		attempt.Status, attempt.AttemptFingerprint, attempt.Attempts,
		attempt.ObservedAt.UnixNano(), attempt.UpdatedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("save test attempt %s/%s: %w", attempt.ProfileID, attempt.ExternalID, err)
	}
	return nil
}

func (store *Store) LatestTestAttempt(ctx context.Context, platform core.Platform, profileID core.ProfileID, externalID string) (core.TestAttempt, bool, error) {
	row := store.db.QueryRowContext(ctx, `SELECT test_definition_id, status, attempt_fingerprint, attempts, observed_at, updated_at
		FROM test_attempts WHERE platform = ? AND profile_id = ? AND external_id = ?`,
		platform, profileID, externalID)
	attempt := core.TestAttempt{Platform: platform, ProfileID: profileID, ExternalID: externalID}
	var observedAt, updatedAt int64
	if err := row.Scan(&attempt.TestDefinitionID, &attempt.Status, &attempt.AttemptFingerprint, &attempt.Attempts, &observedAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.TestAttempt{}, false, nil
		}
		return core.TestAttempt{}, false, fmt.Errorf("load test attempt %s/%s: %w", profileID, externalID, err)
	}
	attempt.ObservedAt = time.Unix(0, observedAt).UTC()
	attempt.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if err := attempt.Validate(); err != nil {
		return core.TestAttempt{}, false, err
	}
	return attempt, true, nil
}
