package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

// ResumeUpdateWorkflow schedules a declared resume update. The durable task
// applies the proposal and optionally publishes the resume afterwards.
type ResumeUpdateWorkflow struct {
	tasks broker.TaskStore
	clock Clock
	ids   IDGenerator
}

func NewResumeUpdateWorkflow(tasks broker.TaskStore, clock Clock, ids IDGenerator) (*ResumeUpdateWorkflow, error) {
	if tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("resume update workflow requires task store, clock and id generator")
	}
	return &ResumeUpdateWorkflow{tasks: tasks, clock: clock, ids: ids}, nil
}

func (workflow *ResumeUpdateWorkflow) EnqueueUpdate(ctx context.Context, profileID core.ProfileID, resourceTag, resumeID string, publish bool, requestKey string) (core.Task, bool, error) {
	payload := core.ResumeUpdatePayload{ProfileID: profileID, ResourceTag: resourceTag, ResumeID: resumeID, Publish: publish}
	if err := payload.Validate(); err != nil {
		return core.Task{}, false, err
	}
	key, err := core.ResumeUpdateIdempotencyKey(profileID, resourceTag, requestKey)
	if err != nil {
		return core.Task{}, false, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return core.Task{}, false, fmt.Errorf("encode resume update task: %w", err)
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
		ID: core.TaskID(id), Type: core.TaskResumeUpdate, IdempotencyKey: key,
		Source: "resume-api", ProfileID: profileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: encoded,
	}, workflow.clock.Now())
	if err != nil {
		return core.Task{}, false, err
	}
	created, err := workflow.tasks.Enqueue(ctx, task)
	if err != nil || created {
		return task, created, err
	}
	existing, err := workflow.tasks.TaskByIdempotencyKey(ctx, key)
	if err != nil {
		return core.Task{}, false, fmt.Errorf("load idempotent resume update task: %w", err)
	}
	return existing, false, nil
}
