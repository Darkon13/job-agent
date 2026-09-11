package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

// QualificationWorkflow enqueues skill verification steps. Discovery and
// attempt execution are separate: this workflow only schedules catalog sync for
// now, while starting an attempt stays an explicit command.
type QualificationWorkflow struct {
	tasks broker.TaskStore
	clock Clock
	ids   IDGenerator
}

func NewQualificationWorkflow(tasks broker.TaskStore, clock Clock, ids IDGenerator) (*QualificationWorkflow, error) {
	if tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("qualification workflow requires task store, clock and id generator")
	}
	return &QualificationWorkflow{tasks: tasks, clock: clock, ids: ids}, nil
}

func (workflow *QualificationWorkflow) EnqueueSync(ctx context.Context, profileID core.ProfileID, requestKey string) (core.Task, bool, error) {
	key, err := core.SkillVerificationSyncIdempotencyKey(profileID, requestKey)
	if err != nil {
		return core.Task{}, false, err
	}
	payload, err := json.Marshal(core.SkillVerificationSyncPayload{ProfileID: profileID})
	if err != nil {
		return core.Task{}, false, fmt.Errorf("encode skill verification sync task: %w", err)
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
		ID: core.TaskID(id), Type: core.TaskSkillVerificationSync, IdempotencyKey: key,
		Source: "qualification-api", ProfileID: profileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
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
		return core.Task{}, false, fmt.Errorf("load idempotent skill verification sync task: %w", err)
	}
	return existing, false, nil
}

// EnqueueStart schedules one explicitly selected attempt. Starting consumes a
// limited or timed attempt, so it is never scheduled by discovery.
func (workflow *QualificationWorkflow) EnqueueStart(ctx context.Context, profileID core.ProfileID, platform core.Platform, offeringID core.QualificationID, requestKey string) (core.Task, bool, error) {
	key, err := core.SkillVerificationStartIdempotencyKey(profileID, offeringID, requestKey)
	if err != nil {
		return core.Task{}, false, err
	}
	payload, err := json.Marshal(core.SkillVerificationStartPayload{ProfileID: profileID, Platform: platform, OfferingID: offeringID})
	if err != nil {
		return core.Task{}, false, fmt.Errorf("encode skill verification start task: %w", err)
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
		ID: core.TaskID(id), Type: core.TaskSkillVerificationStart, IdempotencyKey: key,
		Source: "qualification-api", Platform: platform, ProfileID: profileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
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
		return core.Task{}, false, fmt.Errorf("load idempotent skill verification start task: %w", err)
	}
	return existing, false, nil
}
