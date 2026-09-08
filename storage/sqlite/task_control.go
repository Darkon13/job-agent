package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

func (store *Store) RestartFailedTask(ctx context.Context, key string, now time.Time) (core.Task, error) {
	task, err := store.failedTaskForControl(ctx, key, now)
	if err != nil {
		return core.Task{}, err
	}
	previousUpdatedAt := task.UpdatedAt
	if task.Deadline != nil && !now.Before(*task.Deadline) {
		return core.Task{}, broker.ErrTaskDeadlineExpired
	}
	if err := task.RestartFailed(now); err != nil {
		return core.Task{}, err
	}
	result, err := store.db.ExecContext(ctx, `UPDATE tasks SET
		status = ?, attempts = ?, available_at = ?, updated_at = ?,
		failure_category = NULL, failure_message = NULL,
		lease_owner = NULL, lease_token = NULL, lease_until = NULL
		WHERE idempotency_key = ? AND status = ? AND updated_at = ?`,
		task.Status, task.Attempts, task.AvailableAt.UnixNano(), task.UpdatedAt.UnixNano(),
		key, core.TaskFailed, previousUpdatedAt.UnixNano())
	if err != nil {
		return core.Task{}, fmt.Errorf("restart failed task: %w", err)
	}
	updated, err := oneRowAffected(result)
	if err != nil {
		return core.Task{}, err
	}
	if !updated {
		return core.Task{}, broker.ErrTaskControlConflict
	}
	return task, nil
}

func (store *Store) DismissFailedTask(ctx context.Context, key string, now time.Time) (core.Task, error) {
	task, err := store.failedTaskForControl(ctx, key, now)
	if err != nil {
		return core.Task{}, err
	}
	previousUpdatedAt := task.UpdatedAt
	if err := task.DismissFailure(now); err != nil {
		return core.Task{}, err
	}
	result, err := store.db.ExecContext(ctx, `UPDATE tasks SET
		status = ?, updated_at = ?, lease_owner = NULL, lease_token = NULL, lease_until = NULL
		WHERE idempotency_key = ? AND status = ? AND updated_at = ?`,
		task.Status, task.UpdatedAt.UnixNano(), key, core.TaskFailed, previousUpdatedAt.UnixNano())
	if err != nil {
		return core.Task{}, fmt.Errorf("dismiss failed task: %w", err)
	}
	updated, err := oneRowAffected(result)
	if err != nil {
		return core.Task{}, err
	}
	if !updated {
		return core.Task{}, broker.ErrTaskControlConflict
	}
	return task, nil
}

func (store *Store) failedTaskForControl(ctx context.Context, key string, now time.Time) (core.Task, error) {
	if key == "" || now.IsZero() {
		return core.Task{}, errors.New("failed task control requires idempotency key and current time")
	}
	task, err := store.TaskByIdempotencyKey(ctx, key)
	if err != nil {
		return core.Task{}, err
	}
	if task.Status != core.TaskFailed {
		return core.Task{}, broker.ErrTaskNotFailed
	}
	return task, nil
}
