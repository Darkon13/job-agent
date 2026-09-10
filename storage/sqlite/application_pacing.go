package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func (store *Store) AcquireApplicationPace(ctx context.Context, params core.AcquireApplicationPaceParams) (core.ApplicationPaceReservation, bool, error) {
	if err := params.Validate(); err != nil {
		return core.ApplicationPaceReservation{}, false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ApplicationPaceReservation{}, false, fmt.Errorf("begin application pacing reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The no-op write acquires SQLite's single-writer lock before any aggregate
	// reads, so concurrent profiles cannot reserve the same point in the lane.
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET updated_at = updated_at WHERE id = ?`, params.ApplicationID); err != nil {
		return core.ApplicationPaceReservation{}, false, fmt.Errorf("lock application pacing reservation: %w", err)
	}
	reservation, found, err := applicationPaceInTx(ctx, tx, params.ApplicationID)
	if err != nil {
		return core.ApplicationPaceReservation{}, false, err
	}
	if found && (reservation.ProfileID != params.ProfileID || reservation.Platform != params.Platform) {
		return core.ApplicationPaceReservation{}, false, errors.New("application pacing reservation conflicts with another profile or platform")
	}
	if !found {
		tail, err := applicationPaceTailInTx(ctx, tx, params.ProfileID, params.Platform, params.ApplicationID, params.Now)
		if err != nil {
			return core.ApplicationPaceReservation{}, false, err
		}
		reservation = core.ApplicationPaceReservation{
			ApplicationID: params.ApplicationID, ProfileID: params.ProfileID, Platform: params.Platform,
			ScheduledAt: tail, Interval: params.Interval,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO application_pacing_reservations
			(application_id, profile_id, platform, scheduled_at, interval_ns, acquired_at)
			VALUES (?, ?, ?, ?, ?, NULL)`, reservation.ApplicationID, reservation.ProfileID,
			reservation.Platform, reservation.ScheduledAt.UnixNano(), int64(reservation.Interval)); err != nil {
			return core.ApplicationPaceReservation{}, false, fmt.Errorf("store application pacing reservation: %w", err)
		}
	}

	allowed := false
	if !reservation.ScheduledAt.After(params.Now) {
		nextAllowed, err := applicationPaceNextAllowedInTx(ctx, tx, params.ProfileID, params.Platform, params.ApplicationID, reservation)
		if err != nil {
			return core.ApplicationPaceReservation{}, false, err
		}
		if nextAllowed.After(params.Now) {
			reservation.ScheduledAt, err = applicationPaceTailInTx(ctx, tx, params.ProfileID, params.Platform, params.ApplicationID, nextAllowed)
			if err != nil {
				return core.ApplicationPaceReservation{}, false, err
			}
			reservation.AcquiredAt = nil
			if _, err := tx.ExecContext(ctx, `UPDATE application_pacing_reservations
				SET scheduled_at = ?, acquired_at = NULL WHERE application_id = ?`,
				reservation.ScheduledAt.UnixNano(), reservation.ApplicationID); err != nil {
				return core.ApplicationPaceReservation{}, false, fmt.Errorf("reschedule application pacing reservation: %w", err)
			}
		} else {
			acquiredAt := params.Now
			reservation.AcquiredAt = &acquiredAt
			allowed = true
			if _, err := tx.ExecContext(ctx, `UPDATE application_pacing_reservations
				SET acquired_at = ? WHERE application_id = ?`, acquiredAt.UnixNano(), reservation.ApplicationID); err != nil {
				return core.ApplicationPaceReservation{}, false, fmt.Errorf("acquire application pacing reservation: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return core.ApplicationPaceReservation{}, false, fmt.Errorf("commit application pacing reservation: %w", err)
	}
	return reservation, allowed, nil
}

func applicationPaceInTx(ctx context.Context, tx *sql.Tx, applicationID core.ApplicationID) (core.ApplicationPaceReservation, bool, error) {
	var reservation core.ApplicationPaceReservation
	var scheduledAt int64
	var interval int64
	var acquiredAt sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT application_id, profile_id, platform, scheduled_at, interval_ns, acquired_at
		FROM application_pacing_reservations WHERE application_id = ?`, applicationID).
		Scan(&reservation.ApplicationID, &reservation.ProfileID, &reservation.Platform, &scheduledAt, &interval, &acquiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ApplicationPaceReservation{}, false, nil
	}
	if err != nil {
		return core.ApplicationPaceReservation{}, false, fmt.Errorf("load application pacing reservation: %w", err)
	}
	reservation.ScheduledAt = time.Unix(0, scheduledAt).UTC()
	reservation.Interval = time.Duration(interval)
	if acquiredAt.Valid {
		value := time.Unix(0, acquiredAt.Int64).UTC()
		reservation.AcquiredAt = &value
	}
	return reservation, true, nil
}

func applicationPaceTailInTx(ctx context.Context, tx *sql.Tx, profileID core.ProfileID, platform core.Platform, exclude core.ApplicationID, floor time.Time) (time.Time, error) {
	var tail sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(scheduled_at + interval_ns)
		FROM application_pacing_reservations
		WHERE profile_id = ? AND platform = ? AND application_id <> ?`, profileID, platform, exclude).Scan(&tail); err != nil {
		return time.Time{}, fmt.Errorf("load application pacing tail: %w", err)
	}
	if tail.Valid {
		candidate := time.Unix(0, tail.Int64).UTC()
		if candidate.After(floor) {
			return candidate, nil
		}
	}
	return floor, nil
}

func applicationPaceNextAllowedInTx(ctx context.Context, tx *sql.Tx, profileID core.ProfileID, platform core.Platform, exclude core.ApplicationID, current core.ApplicationPaceReservation) (time.Time, error) {
	var next sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(acquired_at + interval_ns)
		FROM application_pacing_reservations
		WHERE profile_id = ? AND platform = ? AND application_id <> ? AND acquired_at IS NOT NULL`,
		profileID, platform, exclude).Scan(&next); err != nil {
		return time.Time{}, fmt.Errorf("load application pacing gate: %w", err)
	}
	var result time.Time
	if next.Valid {
		result = time.Unix(0, next.Int64).UTC()
	}
	if current.AcquiredAt != nil {
		candidate := current.AcquiredAt.Add(current.Interval)
		if candidate.After(result) {
			result = candidate
		}
	}
	return result, nil
}
