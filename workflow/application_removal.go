package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type ApplicationRemovalWorkflow struct {
	applications storage.ApplicationReadRepository
	tasks        broker.TaskStore
	clock        Clock
	ids          IDGenerator
}

func NewApplicationRemovalWorkflow(applications storage.ApplicationReadRepository, tasks broker.TaskStore, clock Clock, ids IDGenerator) (*ApplicationRemovalWorkflow, error) {
	if applications == nil || tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("application removal workflow requires applications, tasks, clock and ids")
	}
	return &ApplicationRemovalWorkflow{applications: applications, tasks: tasks, clock: clock, ids: ids}, nil
}

func (workflow *ApplicationRemovalWorkflow) Enqueue(ctx context.Context, applicationID core.ApplicationID, requestKey string) (core.Task, bool, error) {
	if requestKey == "" {
		return core.Task{}, false, errors.New("application removal requires request key")
	}
	// Explicit manual deletion has one durable desired-state task per object.
	return workflow.EnqueueRemoval(ctx, core.ApplicationRemovePayload{ApplicationID: applicationID, Reason: core.ApplicationRemovalManual}, "manual")
}

func (workflow *ApplicationRemovalWorkflow) EnqueueRemoval(ctx context.Context, command core.ApplicationRemovePayload, requestKey string) (core.Task, bool, error) {
	if err := command.Validate(); err != nil {
		return core.Task{}, false, err
	}
	applicationID := command.ApplicationID
	idempotencyKey, err := core.ApplicationRemoveIdempotencyKey(applicationID, requestKey)
	if err != nil {
		return core.Task{}, false, err
	}
	// Look up the task before its target: an acknowledged deletion has no row.
	if existing, err := workflow.tasks.TaskByIdempotencyKey(ctx, idempotencyKey); err == nil {
		var previous core.ApplicationRemovePayload
		if err := json.Unmarshal(existing.Payload, &previous); err != nil || previous != command {
			return core.Task{}, false, errors.New("application removal key conflicts with another command")
		}
		return existing, false, nil
	}
	application, err := workflow.applications.ApplicationByID(ctx, applicationID)
	if err != nil {
		return core.Task{}, false, fmt.Errorf("load application removal target: %w", err)
	}
	switch application.Status {
	case core.ApplicationWaitingValidation, core.ApplicationWaitingApproval, core.ApplicationSubmitted,
		core.ApplicationDryRun, core.ApplicationSkipped, core.ApplicationFailed:
	default:
		return core.Task{}, false, fmt.Errorf("application %s is still active in status %s", application.ID, application.Status)
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return core.Task{}, false, fmt.Errorf("encode application remove payload: %w", err)
	}
	taskID, err := workflow.ids.NewID("task")
	if err != nil {
		return core.Task{}, false, err
	}
	correlationID, err := workflow.ids.NewID("correlation")
	if err != nil {
		return core.Task{}, false, err
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(taskID), Type: core.TaskApplicationRemove,
		IdempotencyKey: idempotencyKey, Source: "application-api",
		Platform: application.Key.Vacancy.Platform, ProfileID: application.Key.ProfileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
	}, workflow.clock.Now())
	if err != nil {
		return core.Task{}, false, err
	}
	created, err := workflow.tasks.Enqueue(ctx, task)
	if err != nil || created {
		return task, created, err
	}
	existing, err := workflow.tasks.TaskByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return core.Task{}, false, fmt.Errorf("load idempotent application remove task: %w", err)
	}
	return existing, false, nil
}
