package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func (store *Store) SaveApplicationPlatformState(ctx context.Context, candidate core.ApplicationPlatformState) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	_, err := store.db.ExecContext(ctx, `INSERT INTO application_platform_states(
		application_id, external_negotiation_id, platform_state, disposition,
		viewed_by_opponent, platform_updated_at, observed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(application_id) DO UPDATE SET
		external_negotiation_id = excluded.external_negotiation_id,
		platform_state = excluded.platform_state,
		disposition = excluded.disposition,
		viewed_by_opponent = excluded.viewed_by_opponent,
		platform_updated_at = excluded.platform_updated_at,
		observed_at = excluded.observed_at
	WHERE excluded.observed_at >= application_platform_states.observed_at`,
		candidate.ApplicationID, candidate.ExternalNegotiationID, candidate.PlatformState, candidate.Disposition,
		nullableBool(candidate.ViewedByOpponent), nullableTime(candidate.PlatformUpdatedAt), candidate.ObservedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("save application platform state: %w", err)
	}
	return nil
}

func (store *Store) ApplicationPlatformState(ctx context.Context, applicationID core.ApplicationID) (core.ApplicationPlatformState, error) {
	if applicationID == "" {
		return core.ApplicationPlatformState{}, errors.New("application id is required")
	}
	row := store.db.QueryRowContext(ctx, `SELECT application_id, external_negotiation_id, platform_state,
		disposition, viewed_by_opponent, platform_updated_at, observed_at
		FROM application_platform_states WHERE application_id = ?`, applicationID)
	return scanApplicationPlatformState(row)
}

func (store *Store) ListApplicationsForRetention(ctx context.Context, profileID core.ProfileID) ([]core.Application, error) {
	if profileID == "" {
		return nil, errors.New("application retention requires profile")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id, profile_id, platform, external_id, status, attempts, external_negotiation_id,
		failure_category, failure_message, decision_code, decision_reason, prepared_resume_id, prepared_message,
		preparation_provenance, created_at, updated_at, prepared_at, submitted_at
		FROM applications WHERE profile_id = ? AND status = ?
		ORDER BY submitted_at ASC, id ASC`, profileID, core.ApplicationSubmitted)
	if err != nil {
		return nil, fmt.Errorf("list applications for retention: %w", err)
	}
	defer rows.Close()
	result := make([]core.Application, 0)
	for rows.Next() {
		application, err := scanApplication(rows)
		if err != nil {
			return nil, fmt.Errorf("scan retention application: %w", err)
		}
		result = append(result, application)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retention applications: %w", err)
	}
	return result, nil
}

func scanApplicationPlatformState(row rowScanner) (core.ApplicationPlatformState, error) {
	var state core.ApplicationPlatformState
	var viewed sql.NullBool
	var platformUpdated sql.NullInt64
	var observed int64
	if err := row.Scan(&state.ApplicationID, &state.ExternalNegotiationID, &state.PlatformState,
		&state.Disposition, &viewed, &platformUpdated, &observed); err != nil {
		return core.ApplicationPlatformState{}, err
	}
	if viewed.Valid {
		state.ViewedByOpponent = new(bool)
		*state.ViewedByOpponent = viewed.Bool
	}
	state.PlatformUpdatedAt = timeFromNull(platformUpdated)
	state.ObservedAt = time.Unix(0, observed).UTC()
	return state, state.Validate()
}

func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func (store *Store) ApplicationTombstone(ctx context.Context, id core.ApplicationID) (core.ApplicationTombstone, bool, error) {
	var tombstone core.ApplicationTombstone
	var removedAt int64
	err := store.db.QueryRowContext(ctx, `SELECT application_id, profile_id, platform, external_id, reason, removed_at, status
		FROM application_tombstones WHERE application_id = ?`, id).Scan(&tombstone.ApplicationID,
		&tombstone.Key.ProfileID, &tombstone.Key.Vacancy.Platform, &tombstone.Key.Vacancy.ExternalID,
		&tombstone.Reason, &removedAt, &tombstone.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return tombstone, false, nil
	}
	if err != nil {
		return tombstone, false, err
	}
	tombstone.RemovedAt = time.Unix(0, removedAt).UTC()
	return tombstone, true, tombstone.Validate()
}
