package broker

import (
	"context"
	"errors"
	"time"

	"github.com/Darkon13/job-agent/core"
)

var (
	ErrLeaseLost           = errors.New("task lease is no longer active")
	ErrTaskNotFound        = errors.New("task not found")
	ErrTaskNotFailed       = errors.New("task is not failed")
	ErrTaskDeadlineExpired = errors.New("task deadline has expired")
	ErrTaskControlConflict = errors.New("task changed during operator control")
)

type TaskQueue interface {
	// Enqueue deduplicates against the durable idempotency key, not the task ID.
	Enqueue(ctx context.Context, task core.Task) (created bool, err error)
}

type TaskStore interface {
	TaskQueue
	TaskByIdempotencyKey(ctx context.Context, key string) (core.Task, error)
}

// TaskControlStore exposes narrow operator recovery for durable failures.
// Normal workers still mutate tasks only through an active lease.
type TaskControlStore interface {
	TaskStore
	TaskByID(ctx context.Context, id core.TaskID) (core.Task, error)
	RestartFailedTask(ctx context.Context, idempotencyKey string, now time.Time) (core.Task, error)
	DismissFailedTask(ctx context.Context, idempotencyKey string, now time.Time) (core.Task, error)
}

type ClaimParams struct {
	WorkerID string
	TaskType core.TaskType
	// BlockedByTaskType keeps a task unclaimed while an active task of this
	// type exists for the same profile or its latest terminal result is failed.
	// Waiting does not consume attempts; retry or dismissal is explicit.
	BlockedByTaskType core.TaskType
	Now               time.Time
	LeaseDuration     time.Duration
}

func (params ClaimParams) Validate() error {
	if params.WorkerID == "" {
		return errors.New("task claim requires worker id")
	}
	if params.Now.IsZero() {
		return errors.New("task claim requires current time")
	}
	if params.LeaseDuration <= 0 {
		return errors.New("task claim requires positive lease duration")
	}
	if params.BlockedByTaskType != "" && params.TaskType == "" {
		return errors.New("task claim blocker requires task type")
	}
	if params.BlockedByTaskType != "" && params.BlockedByTaskType == params.TaskType {
		return errors.New("task claim cannot be blocked by its own type")
	}
	return nil
}

type TaskLease struct {
	Task     core.Task
	WorkerID string
	Token    string
	Until    time.Time
}

type TaskConsumer interface {
	Claim(ctx context.Context, params ClaimParams) (lease TaskLease, found bool, err error)
	Extend(ctx context.Context, lease TaskLease, now, until time.Time) (TaskLease, error)
	Complete(ctx context.Context, lease TaskLease, now time.Time) error
	Retry(ctx context.Context, lease TaskLease, operationError *core.OperationError, retryAt, now time.Time) error
	Fail(ctx context.Context, lease TaskLease, operationError *core.OperationError, now time.Time) error
}
