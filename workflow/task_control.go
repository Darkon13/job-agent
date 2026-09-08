package workflow

import (
	"context"
	"errors"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

// TaskControlWorkflow resolves public task IDs to internal idempotency keys and
// provides explicit recovery for terminal failures. Payloads never cross this
// boundary.
type TaskControlWorkflow struct {
	tasks broker.TaskControlStore
	clock Clock
}

func NewTaskControlWorkflow(tasks broker.TaskControlStore, clock Clock) (*TaskControlWorkflow, error) {
	if tasks == nil || clock == nil {
		return nil, errors.New("task control workflow requires task store and clock")
	}
	return &TaskControlWorkflow{tasks: tasks, clock: clock}, nil
}

func (workflow *TaskControlWorkflow) Retry(ctx context.Context, taskID core.TaskID) (core.Task, error) {
	if workflow == nil || taskID == "" {
		return core.Task{}, errors.New("task retry requires workflow and task id")
	}
	target, err := workflow.tasks.TaskByID(ctx, taskID)
	if err != nil {
		return core.Task{}, err
	}
	updated, err := workflow.tasks.RestartFailedTask(ctx, target.IdempotencyKey, workflow.clock.Now())
	if err != nil {
		return core.Task{}, err
	}
	if updated.ID != taskID {
		return core.Task{}, broker.ErrTaskControlConflict
	}
	return updated, nil
}

func (workflow *TaskControlWorkflow) Dismiss(ctx context.Context, taskID core.TaskID) (core.Task, error) {
	if workflow == nil || taskID == "" {
		return core.Task{}, errors.New("task dismissal requires workflow and task id")
	}
	target, err := workflow.tasks.TaskByID(ctx, taskID)
	if err != nil {
		return core.Task{}, err
	}
	updated, err := workflow.tasks.DismissFailedTask(ctx, target.IdempotencyKey, workflow.clock.Now())
	if err != nil {
		return core.Task{}, err
	}
	if updated.ID != taskID {
		return core.Task{}, broker.ErrTaskControlConflict
	}
	return updated, nil
}
