package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

var _ broker.TaskConsumer = (*Store)(nil)

const taskColumns = `id, type, status, idempotency_key, source, platform,
	profile_id, correlation_id, payload, attempts, available_at, deadline, created_at, updated_at,
	failure_category, failure_message`

func (store *Store) Claim(ctx context.Context, params broker.ClaimParams) (broker.TaskLease, bool, error) {
	if err := params.Validate(); err != nil {
		return broker.TaskLease{}, false, err
	}
	requestedUntil := params.Now.Add(params.LeaseDuration)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return broker.TaskLease{}, false, fmt.Errorf("begin task claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET
		status = ?, updated_at = ?, failure_category = ?, failure_message = ?,
		lease_owner = NULL, lease_token = NULL, lease_until = NULL
		WHERE deadline IS NOT NULL AND deadline <= ? AND status IN (?, ?, ?, ?)`,
		core.TaskFailed, params.Now.UnixNano(), core.ErrorPermanentFailure, "task deadline expired",
		params.Now.UnixNano(), core.TaskNew, core.TaskProcessing, core.TaskWaitingConfirmation, core.TaskRetryScheduled); err != nil {
		return broker.TaskLease{}, false, fmt.Errorf("expire overdue tasks: %w", err)
	}

	row := tx.QueryRowContext(ctx, `UPDATE tasks SET
		status = ?, attempts = attempts + 1, updated_at = ?,
		failure_category = NULL, failure_message = NULL,
		lease_owner = ?, lease_token = lower(hex(randomblob(16))),
		lease_until = CASE WHEN deadline IS NOT NULL AND deadline < ? THEN deadline ELSE ? END
		WHERE id = (
			SELECT id FROM tasks
			WHERE (
				(status IN (?, ?) AND available_at <= ?)
				OR (status = ? AND (lease_until IS NULL OR lease_until <= ?))
			)
			AND (? = '' OR type = ?)
			AND (deadline IS NULL OR deadline > ?)
			ORDER BY
				CASE WHEN status = ? THEN COALESCE(lease_until, 0) ELSE available_at END,
				created_at, id
			LIMIT 1
		)
		RETURNING `+taskColumns+`, lease_owner, lease_token, lease_until`,
		core.TaskProcessing, params.Now.UnixNano(), params.WorkerID, requestedUntil.UnixNano(), requestedUntil.UnixNano(),
		core.TaskNew, core.TaskRetryScheduled, params.Now.UnixNano(),
		core.TaskProcessing, params.Now.UnixNano(), params.TaskType, params.TaskType,
		params.Now.UnixNano(), core.TaskProcessing)
	lease, err := scanTaskLease(row)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return broker.TaskLease{}, false, fmt.Errorf("commit empty task claim: %w", err)
		}
		return broker.TaskLease{}, false, nil
	}
	if err != nil {
		return broker.TaskLease{}, false, fmt.Errorf("claim task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return broker.TaskLease{}, false, fmt.Errorf("commit task claim: %w", err)
	}
	return lease, true, nil
}

func (store *Store) Extend(ctx context.Context, lease broker.TaskLease, now, until time.Time) (broker.TaskLease, error) {
	if err := validateActiveLease(lease, now); err != nil {
		return broker.TaskLease{}, err
	}
	if !until.After(lease.Until) {
		return broker.TaskLease{}, errors.New("extended lease must end after current lease")
	}
	if lease.Task.Deadline != nil && until.After(*lease.Task.Deadline) {
		return broker.TaskLease{}, errors.New("extended lease must not exceed task deadline")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE tasks SET lease_until = ?, updated_at = ?
		WHERE id = ? AND status = ? AND lease_owner = ? AND lease_token = ? AND lease_until > ?`,
		until.UnixNano(), now.UnixNano(), lease.Task.ID, core.TaskProcessing,
		lease.WorkerID, lease.Token, now.UnixNano())
	if err != nil {
		return broker.TaskLease{}, fmt.Errorf("extend task lease: %w", err)
	}
	if err := requireLeaseUpdate(result); err != nil {
		return broker.TaskLease{}, err
	}
	lease.Until = until
	lease.Task.UpdatedAt = now
	return lease, nil
}

func (store *Store) Complete(ctx context.Context, lease broker.TaskLease, now time.Time) error {
	if err := validateActiveLease(lease, now); err != nil {
		return err
	}
	task := lease.Task
	if err := task.Transition(core.TaskCompleted, now); err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `UPDATE tasks SET
		status = ?, updated_at = ?, failure_category = NULL, failure_message = NULL,
		lease_owner = NULL, lease_token = NULL, lease_until = NULL
		WHERE id = ? AND status = ? AND lease_owner = ? AND lease_token = ? AND lease_until > ?`,
		task.Status, task.UpdatedAt.UnixNano(), task.ID, core.TaskProcessing,
		lease.WorkerID, lease.Token, now.UnixNano())
	if err != nil {
		return fmt.Errorf("complete task: %w", err)
	}
	return requireLeaseUpdate(result)
}

func (store *Store) Retry(
	ctx context.Context,
	lease broker.TaskLease,
	operationError *core.OperationError,
	retryAt, now time.Time,
) error {
	if err := validateActiveLease(lease, now); err != nil {
		return err
	}
	task := lease.Task
	if err := task.ScheduleRetry(retryAt, operationError, now); err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `UPDATE tasks SET
		status = ?, available_at = ?, updated_at = ?, failure_category = ?, failure_message = ?,
		lease_owner = NULL, lease_token = NULL, lease_until = NULL
		WHERE id = ? AND status = ? AND lease_owner = ? AND lease_token = ? AND lease_until > ?`,
		task.Status, task.AvailableAt.UnixNano(), task.UpdatedAt.UnixNano(), task.Failure.Category, task.Failure.Message,
		task.ID, core.TaskProcessing, lease.WorkerID, lease.Token, now.UnixNano())
	if err != nil {
		return fmt.Errorf("retry task: %w", err)
	}
	return requireLeaseUpdate(result)
}

func (store *Store) Fail(ctx context.Context, lease broker.TaskLease, operationError *core.OperationError, now time.Time) error {
	if err := validateActiveLease(lease, now); err != nil {
		return err
	}
	task := lease.Task
	if err := task.Fail(operationError, now); err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `UPDATE tasks SET
		status = ?, updated_at = ?, failure_category = ?, failure_message = ?,
		lease_owner = NULL, lease_token = NULL, lease_until = NULL
		WHERE id = ? AND status = ? AND lease_owner = ? AND lease_token = ? AND lease_until > ?`,
		task.Status, task.UpdatedAt.UnixNano(), task.Failure.Category, task.Failure.Message,
		task.ID, core.TaskProcessing, lease.WorkerID, lease.Token, now.UnixNano())
	if err != nil {
		return fmt.Errorf("fail task: %w", err)
	}
	return requireLeaseUpdate(result)
}

func scanTaskLease(row rowScanner) (broker.TaskLease, error) {
	var lease broker.TaskLease
	var payload []byte
	var availableAt, createdAt, updatedAt, leaseUntil int64
	var deadline sql.NullInt64
	var failureCategory, failureMessage sql.NullString
	if err := row.Scan(
		&lease.Task.ID, &lease.Task.Type, &lease.Task.Status, &lease.Task.IdempotencyKey,
		&lease.Task.Source, &lease.Task.Platform, &lease.Task.ProfileID, &lease.Task.CorrelationID,
		&payload, &lease.Task.Attempts, &availableAt, &deadline, &createdAt, &updatedAt,
		&failureCategory, &failureMessage, &lease.WorkerID, &lease.Token, &leaseUntil,
	); err != nil {
		return broker.TaskLease{}, err
	}
	lease.Task.Payload = append(lease.Task.Payload[:0], payload...)
	lease.Task.AvailableAt = time.Unix(0, availableAt).UTC()
	lease.Task.Deadline = timeFromNull(deadline)
	lease.Task.CreatedAt = time.Unix(0, createdAt).UTC()
	lease.Task.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if failureCategory.Valid {
		lease.Task.Failure = &core.TaskFailure{Category: core.ErrorCategory(failureCategory.String), Message: failureMessage.String}
	}
	lease.Until = time.Unix(0, leaseUntil).UTC()
	return lease, nil
}

func validateActiveLease(lease broker.TaskLease, now time.Time) error {
	if lease.Task.ID == "" || lease.WorkerID == "" || lease.Token == "" || lease.Until.IsZero() {
		return errors.New("task lease is incomplete")
	}
	if lease.Task.Status != core.TaskProcessing {
		return errors.New("task lease does not contain a processing task")
	}
	if now.IsZero() {
		return errors.New("task lease operation requires current time")
	}
	if !now.Before(lease.Until) {
		return broker.ErrLeaseLost
	}
	if lease.Task.Deadline != nil && !now.Before(*lease.Task.Deadline) {
		return broker.ErrLeaseLost
	}
	return nil
}

func requireLeaseUpdate(result sql.Result) error {
	updated, err := oneRowAffected(result)
	if err != nil {
		return err
	}
	if !updated {
		return broker.ErrLeaseLost
	}
	return nil
}
