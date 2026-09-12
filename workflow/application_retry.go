package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ApplicationRetryWorkflow resets one blocked application to a fresh
// preparation and enqueues a new idempotent application.submit task. The
// platform action itself stays inside the durable worker.
type ApplicationRetryWorkflow struct {
	applications applicationRetryRepository
	tasks        broker.TaskStore
	clock        Clock
	ids          IDGenerator
}

type applicationRetryRepository interface {
	storage.ApplicationRepository
	storage.ApplicationReadRepository
}

func NewApplicationRetryWorkflow(applications applicationRetryRepository, tasks broker.TaskStore, clock Clock, ids IDGenerator) (*ApplicationRetryWorkflow, error) {
	if applications == nil || tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("application retry workflow requires applications, task store, clock and id generator")
	}
	return &ApplicationRetryWorkflow{applications: applications, tasks: tasks, clock: clock, ids: ids}, nil
}

// Enqueue is idempotent per (application, request key): a replayed request
// returns the already enqueued task without resetting or submitting twice.
func (workflow *ApplicationRetryWorkflow) Enqueue(ctx context.Context, applicationID core.ApplicationID, requestKey string) (core.Task, bool, error) {
	if workflow == nil {
		return core.Task{}, false, errors.New("application retry workflow is nil")
	}
	applicationID = core.ApplicationID(strings.TrimSpace(string(applicationID)))
	if applicationID == "" {
		return core.Task{}, false, errors.New("application retry requires application")
	}
	idempotencyKey, err := core.ApplicationRetryIdempotencyKey(applicationID, requestKey)
	if err != nil {
		return core.Task{}, false, err
	}
	if existing, lookupErr := workflow.tasks.TaskByIdempotencyKey(ctx, idempotencyKey); lookupErr == nil {
		return existing, false, nil
	} else if !errors.Is(lookupErr, broker.ErrTaskNotFound) {
		return core.Task{}, false, lookupErr
	}
	application, err := workflow.applications.ApplicationByID(ctx, applicationID)
	if err != nil {
		return core.Task{}, false, err
	}
	now := workflow.clock.Now()
	expectedStatus := application.Status
	if err := application.ResetForRetry(now); err != nil {
		return core.Task{}, false, &core.OperationError{
			Category: core.ErrorValidationRequired, Operation: "applications.retry",
			Platform: application.Key.Vacancy.Platform, Message: err.Error(), Cause: err,
		}
	}
	if err := workflow.applications.SaveApplication(ctx, application, expectedStatus); err != nil {
		return core.Task{}, false, fmt.Errorf("reset application for retry: %w", err)
	}
	payload, err := json.Marshal(core.ApplicationSubmitPayload{ApplicationID: application.ID, Key: application.Key})
	if err != nil {
		return core.Task{}, false, fmt.Errorf("encode application retry task: %w", err)
	}
	id, err := workflow.ids.NewID("task")
	if err != nil {
		return core.Task{}, false, err
	}
	correlationID, err := workflow.ids.NewID("correlation")
	if err != nil {
		return core.Task{}, false, err
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(id), Type: core.TaskApplicationSubmit, IdempotencyKey: idempotencyKey,
		Source: "manual:application.retry", Platform: application.Key.Vacancy.Platform,
		ProfileID: application.Key.ProfileID, CorrelationID: core.CorrelationID(correlationID), Payload: payload,
	}, now)
	if err != nil {
		return core.Task{}, false, err
	}
	created, err := workflow.tasks.Enqueue(ctx, task)
	if err != nil || created {
		return task, created, err
	}
	stored, err := workflow.tasks.TaskByIdempotencyKey(ctx, idempotencyKey)
	return stored, false, err
}
