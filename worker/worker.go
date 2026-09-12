package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type Handler interface {
	Handle(ctx context.Context, task core.Task) error
}

type HandlerFunc func(context.Context, core.Task) error

func (function HandlerFunc) Handle(ctx context.Context, task core.Task) error {
	return function(ctx, task)
}

type Config struct {
	ID                string
	TaskType          core.TaskType
	BlockedByTaskType core.TaskType
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	PollInterval      time.Duration
	RetryBaseDelay    time.Duration
	BlockedRetryDelay time.Duration
	MaxAttempts       int
}

func (config Config) Validate() error {
	if config.ID == "" || config.TaskType == "" {
		return errors.New("worker requires id and task type")
	}
	if config.BlockedByTaskType == config.TaskType {
		return errors.New("worker task type cannot block itself")
	}
	if config.LeaseDuration <= 0 || config.HeartbeatInterval <= 0 || config.HeartbeatInterval >= config.LeaseDuration {
		return errors.New("worker requires heartbeat interval shorter than a positive lease duration")
	}
	if config.PollInterval <= 0 || config.RetryBaseDelay <= 0 || config.BlockedRetryDelay <= 0 || config.MaxAttempts < 1 {
		return errors.New("worker requires positive polling, retry delays and max attempts")
	}
	return nil
}

type Worker struct {
	consumer broker.TaskConsumer
	handler  Handler
	clock    Clock
	config   Config
}

func New(consumer broker.TaskConsumer, handler Handler, clock Clock, config Config) (*Worker, error) {
	if consumer == nil || handler == nil || clock == nil {
		return nil, errors.New("worker requires consumer, handler and clock")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Worker{consumer: consumer, handler: handler, clock: clock, config: config}, nil
}

func (worker *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(worker.config.PollInterval)
	defer ticker.Stop()
	for {
		worked, err := worker.RunOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (worker *Worker) RunOnce(ctx context.Context) (bool, error) {
	now := worker.clock.Now()
	lease, found, err := worker.consumer.Claim(ctx, broker.ClaimParams{
		WorkerID: worker.config.ID, TaskType: worker.config.TaskType,
		BlockedByTaskType: worker.config.BlockedByTaskType,
		Now:               now, LeaseDuration: worker.config.LeaseDuration,
	})
	if err != nil || !found {
		return found, err
	}
	if err := worker.execute(ctx, lease); err != nil {
		return true, fmt.Errorf("worker %s task %s: %w", worker.config.ID, lease.Task.ID, err)
	}
	return true, nil
}

func (worker *Worker) execute(ctx context.Context, lease broker.TaskLease) error {
	handlerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- worker.handler.Handle(handlerCtx, lease.Task) }()
	heartbeat := time.NewTicker(worker.config.HeartbeatInterval)
	defer heartbeat.Stop()
	activeLease := lease
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case handlerErr := <-result:
			return worker.finish(ctx, activeLease, handlerErr)
		case <-heartbeat.C:
			now := worker.clock.Now()
			until := now.Add(worker.config.LeaseDuration)
			if activeLease.Task.Deadline != nil && until.After(*activeLease.Task.Deadline) {
				until = *activeLease.Task.Deadline
			}
			if !until.After(activeLease.Until) {
				continue
			}
			extended, err := worker.consumer.Extend(ctx, activeLease, now, until)
			if err != nil {
				cancel()
				return err
			}
			activeLease = extended
		}
	}
}

func (worker *Worker) finish(ctx context.Context, lease broker.TaskLease, handlerErr error) error {
	now := worker.clock.Now()
	if handlerErr == nil {
		return worker.consumer.Complete(ctx, lease, now)
	}
	operationError := normalizeError(handlerErr, lease.Task.Type)
	if retryable(operationError.Category) && lease.Task.Attempts < worker.config.MaxAttempts {
		retryAt := worker.retryAt(*operationError, lease.Task.Attempts, now)
		slog.Default().Warn("task retry scheduled",
			"task", lease.Task.ID, "type", lease.Task.Type, "attempts", lease.Task.Attempts,
			"category", operationError.Category, "retry_at", retryAt.Format(time.RFC3339))
		return worker.consumer.Retry(ctx, lease, operationError, retryAt, now)
	}
	slog.Default().Error("task failed",
		"task", lease.Task.ID, "type", lease.Task.Type, "attempts", lease.Task.Attempts,
		"category", operationError.Category, "error", operationError.Message)
	return worker.consumer.Fail(ctx, lease, operationError, now)
}

func (worker *Worker) retryAt(operationError core.OperationError, attempts int, now time.Time) time.Time {
	if operationError.RetryAfter != nil && operationError.RetryAfter.After(now) {
		return *operationError.RetryAfter
	}
	if operationError.Category == core.ErrorUnauthorized || operationError.Category == core.ErrorValidationRequired || operationError.Category == core.ErrorConfirmationRequired {
		return now.Add(worker.config.BlockedRetryDelay)
	}
	delay := worker.config.RetryBaseDelay
	for attempt := 1; attempt < attempts && delay < time.Hour; attempt++ {
		delay *= 2
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	return now.Add(delay)
}

func normalizeError(err error, taskType core.TaskType) *core.OperationError {
	var operationError *core.OperationError
	if errors.As(err, &operationError) && operationError.Validate() == nil {
		return operationError
	}
	if isSQLiteBusy(err) {
		// Write contention between workers is transient; retrying is correct.
		return &core.OperationError{
			Category: core.ErrorTemporaryFailure, Operation: string(taskType),
			Message: handlerFailureMessage(err), Cause: err,
		}
	}
	return &core.OperationError{
		Category: core.ErrorPermanentFailure, Operation: string(taskType),
		Message: handlerFailureMessage(err), Cause: err,
	}
}

// isSQLiteBusy detects SQLite write contention (SQLITE_BUSY / locked) so it is
// retried as a temporary failure instead of failing the task permanently.
func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToUpper(err.Error())
	return strings.Contains(text, "SQLITE_BUSY") ||
		strings.Contains(text, "DATABASE IS LOCKED") ||
		strings.Contains(text, "DATABASE TABLE IS LOCKED")
}

// handlerFailureMessage keeps the underlying cause visible for operators while
// bounding the stored text. Plain handler errors used to collapse into a bare
// "handler failed", which made permanent failures undiagnosable.
func handlerFailureMessage(err error) string {
	const maximum = 300
	message := "handler failed"
	if err != nil {
		if text := strings.TrimSpace(err.Error()); text != "" {
			if len(text) > maximum {
				text = text[:maximum]
			}
			message = "handler failed: " + text
		}
	}
	return message
}

func retryable(category core.ErrorCategory) bool {
	switch category {
	case core.ErrorTemporaryFailure, core.ErrorRateLimited, core.ErrorQuotaExceeded,
		core.ErrorUnauthorized, core.ErrorValidationRequired, core.ErrorConfirmationRequired, core.ErrorAmbiguousResult:
		return true
	default:
		return false
	}
}
