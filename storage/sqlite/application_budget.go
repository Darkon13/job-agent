package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func (store *Store) ReserveApplicationBudget(ctx context.Context, params core.ReserveApplicationBudgetParams) (core.ApplicationBudgetReservation, error) {
	if err := params.Validate(); err != nil {
		return core.ApplicationBudgetReservation{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ApplicationBudgetReservation{}, fmt.Errorf("begin application budget reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `INSERT INTO application_budget_buckets
		(profile_id, platform, window_start, window_end, limit_value, used)
		VALUES (?, ?, ?, ?, ?, 0)
		ON CONFLICT(profile_id, platform, window_start) DO UPDATE SET
			limit_value = MAX(application_budget_buckets.used,
				MIN(application_budget_buckets.limit_value, excluded.limit_value))`,
		params.ProfileID, params.Platform, params.WindowStart.UnixNano(), params.WindowEnd.UnixNano(), params.Limit)
	if err != nil {
		return core.ApplicationBudgetReservation{}, fmt.Errorf("create application budget bucket: %w", err)
	}
	existing, found, err := applicationBudgetInTx(ctx, tx, params.ApplicationID)
	if err != nil {
		return core.ApplicationBudgetReservation{}, err
	}
	if found && existing.State != core.ApplicationBudgetReleased {
		if existing.ProfileID != params.ProfileID || existing.Platform != params.Platform ||
			!existing.WindowStart.Equal(params.WindowStart) || !existing.WindowEnd.Equal(params.WindowEnd) {
			return core.ApplicationBudgetReservation{}, errors.New("application budget reservation conflicts with another window")
		}
		return existing, tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `UPDATE application_budget_buckets SET used = used + 1
		WHERE profile_id = ? AND platform = ? AND window_start = ? AND used < limit_value`,
		params.ProfileID, params.Platform, params.WindowStart.UnixNano())
	if err != nil {
		return core.ApplicationBudgetReservation{}, fmt.Errorf("reserve application budget slot: %w", err)
	}
	reserved, err := oneRowAffected(result)
	if err != nil {
		return core.ApplicationBudgetReservation{}, err
	}
	if !reserved {
		resetAt := params.WindowEnd
		return core.ApplicationBudgetReservation{}, &core.OperationError{
			Category: core.ErrorQuotaExceeded, Operation: "applications.budget.reserve", Platform: params.Platform,
			RetryAfter: &resetAt, Message: "daily application budget is exhausted",
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO application_budget_reservations
		(application_id, profile_id, platform, window_start, window_end, limit_value, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(application_id) DO UPDATE SET
			profile_id = excluded.profile_id, platform = excluded.platform,
			window_start = excluded.window_start, window_end = excluded.window_end,
			limit_value = excluded.limit_value, state = excluded.state,
			created_at = excluded.created_at, updated_at = excluded.updated_at
		WHERE application_budget_reservations.state = 'released'`,
		params.ApplicationID, params.ProfileID, params.Platform, params.WindowStart.UnixNano(), params.WindowEnd.UnixNano(),
		params.Limit, core.ApplicationBudgetReserved, params.Now.UnixNano(), params.Now.UnixNano())
	if err != nil {
		return core.ApplicationBudgetReservation{}, fmt.Errorf("store application budget reservation: %w", err)
	}
	reservation := core.ApplicationBudgetReservation{
		ApplicationID: params.ApplicationID, ProfileID: params.ProfileID, Platform: params.Platform,
		WindowStart: params.WindowStart, WindowEnd: params.WindowEnd, Limit: params.Limit,
		State: core.ApplicationBudgetReserved, CreatedAt: params.Now, UpdatedAt: params.Now,
	}
	if err := tx.Commit(); err != nil {
		return core.ApplicationBudgetReservation{}, fmt.Errorf("commit application budget reservation: %w", err)
	}
	return reservation, nil
}

func (store *Store) CommitApplicationBudget(ctx context.Context, applicationID core.ApplicationID, now time.Time) error {
	return store.finishApplicationBudget(ctx, applicationID, core.ApplicationBudgetCommitted, now)
}

func (store *Store) ReleaseApplicationBudget(ctx context.Context, applicationID core.ApplicationID, now time.Time) error {
	return store.finishApplicationBudget(ctx, applicationID, core.ApplicationBudgetReleased, now)
}

func (store *Store) finishApplicationBudget(ctx context.Context, applicationID core.ApplicationID, target core.ApplicationBudgetState, now time.Time) error {
	if applicationID == "" || now.IsZero() {
		return errors.New("application budget update requires application and current time")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin application budget update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	reservation, found, err := applicationBudgetInTx(ctx, tx, applicationID)
	if err != nil {
		return err
	}
	if !found {
		if target == core.ApplicationBudgetReleased {
			return tx.Commit()
		}
		return errors.New("application budget reservation not found")
	}
	if now.Before(reservation.UpdatedAt) {
		return errors.New("application budget update time must not move backwards")
	}
	if reservation.State == target {
		return tx.Commit()
	}
	if reservation.State != core.ApplicationBudgetReserved {
		return errors.New("application budget reservation is already final")
	}
	result, err := tx.ExecContext(ctx, `UPDATE application_budget_reservations SET state = ?, updated_at = ?
		WHERE application_id = ? AND state = ?`, target, now.UnixNano(), applicationID, core.ApplicationBudgetReserved)
	if err != nil {
		return fmt.Errorf("finish application budget reservation: %w", err)
	}
	updated, err := oneRowAffected(result)
	if err != nil {
		return err
	}
	if !updated {
		return errors.New("application budget reservation changed concurrently")
	}
	if target == core.ApplicationBudgetReleased {
		_, err = tx.ExecContext(ctx, `UPDATE application_budget_buckets SET used = used - 1
			WHERE profile_id = ? AND platform = ? AND window_start = ? AND used > 0`,
			reservation.ProfileID, reservation.Platform, reservation.WindowStart.UnixNano())
		if err != nil {
			return fmt.Errorf("release application budget slot: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit application budget update: %w", err)
	}
	return nil
}

func applicationBudgetInTx(ctx context.Context, tx *sql.Tx, applicationID core.ApplicationID) (core.ApplicationBudgetReservation, bool, error) {
	var reservation core.ApplicationBudgetReservation
	var windowStart, windowEnd, createdAt, updatedAt int64
	err := tx.QueryRowContext(ctx, `SELECT application_id, profile_id, platform, window_start, window_end,
		limit_value, state, created_at, updated_at FROM application_budget_reservations WHERE application_id = ?`, applicationID).
		Scan(&reservation.ApplicationID, &reservation.ProfileID, &reservation.Platform, &windowStart, &windowEnd,
			&reservation.Limit, &reservation.State, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ApplicationBudgetReservation{}, false, nil
	}
	if err != nil {
		return core.ApplicationBudgetReservation{}, false, fmt.Errorf("load application budget reservation: %w", err)
	}
	reservation.WindowStart = time.Unix(0, windowStart).UTC()
	reservation.WindowEnd = time.Unix(0, windowEnd).UTC()
	reservation.CreatedAt = time.Unix(0, createdAt).UTC()
	reservation.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return reservation, true, nil
}
