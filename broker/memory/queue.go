package memory

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

var (
	_ broker.TaskQueue    = (*Queue)(nil)
	_ broker.TaskConsumer = (*Queue)(nil)
	_ broker.TaskStore    = (*Queue)(nil)
)

type leaseState struct {
	workerID string
	token    string
	until    time.Time
}

type Queue struct {
	mu       sync.RWMutex
	tasks    map[string]core.Task
	taskKeys map[core.TaskID]string
	leases   map[core.TaskID]leaseState
}

func NewQueue() *Queue {
	return &Queue{
		tasks: make(map[string]core.Task), taskKeys: make(map[core.TaskID]string),
		leases: make(map[core.TaskID]leaseState),
	}
}

func (queue *Queue) Enqueue(ctx context.Context, task core.Task) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if task.ID == "" || task.Type == "" || task.IdempotencyKey == "" || task.CorrelationID == "" {
		return false, errors.New("queued task requires id, type, idempotency key and correlation id")
	}
	if task.Status != core.TaskNew || len(task.Payload) == 0 || !json.Valid(task.Payload) {
		return false, errors.New("queue accepts only initialized new tasks with valid JSON payload")
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if existing, exists := queue.tasks[task.IdempotencyKey]; exists {
		if existing.Type != task.Type || existing.Platform != task.Platform || existing.ProfileID != task.ProfileID || !bytes.Equal(existing.Payload, task.Payload) {
			return false, errors.New("task idempotency key conflicts with a different command")
		}
		return false, nil
	}
	if _, exists := queue.taskKeys[task.ID]; exists {
		return false, errors.New("task id conflicts with an existing task")
	}
	queue.tasks[task.IdempotencyKey] = cloneTask(task)
	queue.taskKeys[task.ID] = task.IdempotencyKey
	return true, nil
}

func (queue *Queue) TaskByIdempotencyKey(ctx context.Context, key string) (core.Task, error) {
	if err := ctx.Err(); err != nil {
		return core.Task{}, err
	}
	if key == "" {
		return core.Task{}, errors.New("task idempotency key is required")
	}
	queue.mu.RLock()
	defer queue.mu.RUnlock()
	task, exists := queue.tasks[key]
	if !exists {
		return core.Task{}, errors.New("task not found")
	}
	return cloneTask(task), nil
}

func (queue *Queue) Claim(ctx context.Context, params broker.ClaimParams) (broker.TaskLease, bool, error) {
	if err := ctx.Err(); err != nil {
		return broker.TaskLease{}, false, err
	}
	if err := params.Validate(); err != nil {
		return broker.TaskLease{}, false, err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.expireOverdue(params.Now)

	type candidate struct {
		key       string
		available time.Time
		task      core.Task
	}
	candidates := make([]candidate, 0, len(queue.tasks))
	for key, task := range queue.tasks {
		available, eligible := queue.eligibleAt(task, params.Now)
		if eligible {
			candidates = append(candidates, candidate{key: key, available: available, task: task})
		}
	}
	if len(candidates) == 0 {
		return broker.TaskLease{}, false, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].available.Equal(candidates[j].available) {
			return candidates[i].available.Before(candidates[j].available)
		}
		if !candidates[i].task.CreatedAt.Equal(candidates[j].task.CreatedAt) {
			return candidates[i].task.CreatedAt.Before(candidates[j].task.CreatedAt)
		}
		return candidates[i].task.ID < candidates[j].task.ID
	})
	selected := candidates[0]
	token, err := randomToken()
	if err != nil {
		return broker.TaskLease{}, false, err
	}
	until := params.Now.Add(params.LeaseDuration)
	if selected.task.Deadline != nil && selected.task.Deadline.Before(until) {
		until = *selected.task.Deadline
	}
	selected.task.Status = core.TaskProcessing
	selected.task.Attempts++
	selected.task.UpdatedAt = params.Now
	selected.task.Failure = nil
	queue.tasks[selected.key] = selected.task
	state := leaseState{workerID: params.WorkerID, token: token, until: until}
	queue.leases[selected.task.ID] = state
	return leaseFrom(selected.task, state), true, nil
}

func (queue *Queue) Extend(ctx context.Context, lease broker.TaskLease, now, until time.Time) (broker.TaskLease, error) {
	if err := ctx.Err(); err != nil {
		return broker.TaskLease{}, err
	}
	if err := validateLeaseInput(lease, now); err != nil {
		return broker.TaskLease{}, err
	}
	if !until.After(lease.Until) {
		return broker.TaskLease{}, errors.New("extended lease must end after current lease")
	}
	if lease.Task.Deadline != nil && until.After(*lease.Task.Deadline) {
		return broker.TaskLease{}, errors.New("extended lease must not exceed task deadline")
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	key, task, state, err := queue.activeLease(lease, now)
	if err != nil {
		return broker.TaskLease{}, err
	}
	state.until = until
	task.UpdatedAt = now
	queue.tasks[key] = task
	queue.leases[task.ID] = state
	return leaseFrom(task, state), nil
}

func (queue *Queue) Complete(ctx context.Context, lease broker.TaskLease, now time.Time) error {
	return queue.finish(ctx, lease, now, func(task *core.Task) error {
		return task.Transition(core.TaskCompleted, now)
	})
}

func (queue *Queue) Retry(
	ctx context.Context,
	lease broker.TaskLease,
	operationError *core.OperationError,
	retryAt, now time.Time,
) error {
	return queue.finish(ctx, lease, now, func(task *core.Task) error {
		return task.ScheduleRetry(retryAt, operationError, now)
	})
}

func (queue *Queue) Fail(ctx context.Context, lease broker.TaskLease, operationError *core.OperationError, now time.Time) error {
	return queue.finish(ctx, lease, now, func(task *core.Task) error {
		return task.Fail(operationError, now)
	})
}

func (queue *Queue) finish(ctx context.Context, lease broker.TaskLease, now time.Time, transition func(*core.Task) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateLeaseInput(lease, now); err != nil {
		return err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	key, task, _, err := queue.activeLease(lease, now)
	if err != nil {
		return err
	}
	if err := transition(&task); err != nil {
		return err
	}
	queue.tasks[key] = task
	delete(queue.leases, task.ID)
	return nil
}

func (queue *Queue) activeLease(lease broker.TaskLease, now time.Time) (string, core.Task, leaseState, error) {
	key, exists := queue.taskKeys[lease.Task.ID]
	if !exists {
		return "", core.Task{}, leaseState{}, broker.ErrLeaseLost
	}
	task := queue.tasks[key]
	state, exists := queue.leases[task.ID]
	if !exists || task.Status != core.TaskProcessing || state.workerID != lease.WorkerID || state.token != lease.Token || !now.Before(state.until) {
		return "", core.Task{}, leaseState{}, broker.ErrLeaseLost
	}
	if task.Deadline != nil && !now.Before(*task.Deadline) {
		return "", core.Task{}, leaseState{}, broker.ErrLeaseLost
	}
	return key, task, state, nil
}

func (queue *Queue) eligibleAt(task core.Task, now time.Time) (time.Time, bool) {
	switch task.Status {
	case core.TaskNew, core.TaskRetryScheduled:
		return task.AvailableAt, !now.Before(task.AvailableAt)
	case core.TaskProcessing:
		state, exists := queue.leases[task.ID]
		if !exists || !now.Before(state.until) {
			if exists {
				return state.until, true
			}
			return task.UpdatedAt, true
		}
	}
	return time.Time{}, false
}

func (queue *Queue) expireOverdue(now time.Time) {
	for key, task := range queue.tasks {
		if task.Deadline == nil || now.Before(*task.Deadline) {
			continue
		}
		switch task.Status {
		case core.TaskNew, core.TaskProcessing, core.TaskWaitingConfirmation, core.TaskRetryScheduled:
			task.Status = core.TaskFailed
			task.UpdatedAt = now
			task.Failure = &core.TaskFailure{Category: core.ErrorPermanentFailure, Message: "task deadline expired"}
			queue.tasks[key] = task
			delete(queue.leases, task.ID)
		}
	}
}

func (queue *Queue) Tasks() []core.Task {
	queue.mu.RLock()
	defer queue.mu.RUnlock()
	result := make([]core.Task, 0, len(queue.tasks))
	for _, task := range queue.tasks {
		result = append(result, cloneTask(task))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].IdempotencyKey < result[j].IdempotencyKey })
	return result
}

func cloneTask(task core.Task) core.Task {
	task.Payload = append(json.RawMessage(nil), task.Payload...)
	if task.Deadline != nil {
		value := *task.Deadline
		task.Deadline = &value
	}
	if task.Failure != nil {
		value := *task.Failure
		task.Failure = &value
	}
	return task
}

func leaseFrom(task core.Task, state leaseState) broker.TaskLease {
	return broker.TaskLease{Task: cloneTask(task), WorkerID: state.workerID, Token: state.token, Until: state.until}
}

func validateLeaseInput(lease broker.TaskLease, now time.Time) error {
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

func randomToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
