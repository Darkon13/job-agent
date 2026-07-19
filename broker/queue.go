package broker

import (
	"context"
	"errors"
	"time"

	"github.com/Darkon13/job-agent/core"
)

var ErrLeaseLost = errors.New("task lease is no longer active")

type TaskQueue interface {
	// Enqueue deduplicates against the durable idempotency key, not the task ID.
	Enqueue(ctx context.Context, task core.Task) (created bool, err error)
}

type TaskStore interface {
	TaskQueue
	TaskByIdempotencyKey(ctx context.Context, key string) (core.Task, error)
}

type ClaimParams struct {
	WorkerID      string
	TaskType      core.TaskType
	Now           time.Time
	LeaseDuration time.Duration
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
