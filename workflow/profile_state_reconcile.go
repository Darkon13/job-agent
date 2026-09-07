package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

var ErrProfileStateReconcileUnavailable = errors.New("profile state reconcile is unavailable")

// ProfileStateReconcileWorkflow creates a durable read-plan-apply command.
// The command contains only a resource tag; resolved desired values stay in
// the registered resource and later in the immutable proposal.
type ProfileStateReconcileWorkflow struct {
	planner   *ProfileStatePlanner
	tasks     broker.TaskStore
	clock     Clock
	ids       IDGenerator
	platforms map[core.ProfileID]core.Platform
}

func NewProfileStateReconcileWorkflow(planner *ProfileStatePlanner, tasks broker.TaskStore, clock Clock, ids IDGenerator, platforms map[core.ProfileID]core.Platform) (*ProfileStateReconcileWorkflow, error) {
	if planner == nil || tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("profile state reconcile workflow requires planner, task store, clock and id generator")
	}
	copiedPlatforms := make(map[core.ProfileID]core.Platform, len(platforms))
	for profileID, platform := range platforms {
		if profileID == "" || platform == "" {
			return nil, errors.New("profile state reconcile platforms require profile and platform")
		}
		copiedPlatforms[profileID] = platform
	}
	return &ProfileStateReconcileWorkflow{planner: planner, tasks: tasks, clock: clock, ids: ids, platforms: copiedPlatforms}, nil
}

func (workflow *ProfileStateReconcileWorkflow) Available(resourceTag string) bool {
	if workflow == nil {
		return false
	}
	resource, exists := workflow.planner.Resource(resourceTag)
	if !exists {
		return false
	}
	_, exists = workflow.platforms[resource.ProfileID]
	return exists
}

func (workflow *ProfileStateReconcileWorkflow) Enqueue(ctx context.Context, resourceTag, source, requestKey string) (core.Task, bool, error) {
	if workflow == nil {
		return core.Task{}, false, errors.New("profile state reconcile workflow is nil")
	}
	resourceTag = strings.TrimSpace(resourceTag)
	source = strings.TrimSpace(source)
	resource, exists := workflow.planner.Resource(resourceTag)
	if !exists {
		return core.Task{}, false, fmt.Errorf("profile state resource %q is not registered", resourceTag)
	}
	platform, available := workflow.platforms[resource.ProfileID]
	if !available {
		return core.Task{}, false, ErrProfileStateReconcileUnavailable
	}
	if source == "" {
		return core.Task{}, false, errors.New("profile state reconcile requires source")
	}
	idempotencyKey, err := core.ProfileStateReconcileIdempotencyKey(resourceTag, requestKey)
	if err != nil {
		return core.Task{}, false, err
	}
	payload, err := json.Marshal(core.ProfileStateReconcilePayload{ResourceTag: resourceTag})
	if err != nil {
		return core.Task{}, false, fmt.Errorf("encode profile state reconcile task: %w", err)
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
		ID: core.TaskID(id), Type: core.TaskProfileStateReconcile, IdempotencyKey: idempotencyKey,
		Source: source, Platform: platform, ProfileID: resource.ProfileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
	}, workflow.clock.Now())
	if err != nil {
		return core.Task{}, false, err
	}
	created, err := workflow.tasks.Enqueue(ctx, task)
	if err != nil {
		return core.Task{}, false, err
	}
	if created {
		return task, true, nil
	}
	stored, err := workflow.tasks.TaskByIdempotencyKey(ctx, idempotencyKey)
	return stored, false, err
}

type ProfileStateReconcileHandler struct {
	planner *ProfileStatePlanner
	apply   *ProfileStateApplyWorkflow
	readers map[core.ProfileID]adapter.ProfileStateReader
}

func NewProfileStateReconcileHandler(planner *ProfileStatePlanner, apply *ProfileStateApplyWorkflow, readers map[core.ProfileID]adapter.ProfileStateReader) (*ProfileStateReconcileHandler, error) {
	if planner == nil || apply == nil {
		return nil, errors.New("profile state reconcile handler requires planner and apply workflow")
	}
	copiedReaders := make(map[core.ProfileID]adapter.ProfileStateReader, len(readers))
	for profileID, reader := range readers {
		if profileID == "" || reader == nil {
			return nil, errors.New("profile state reconcile readers require profile and implementation")
		}
		copiedReaders[profileID] = reader
	}
	return &ProfileStateReconcileHandler{planner: planner, apply: apply, readers: copiedReaders}, nil
}

func (handler *ProfileStateReconcileHandler) Handle(ctx context.Context, task core.Task) error {
	if task.Type != core.TaskProfileStateReconcile {
		return errors.New("profile state reconcile handler received another task type")
	}
	var payload core.ProfileStateReconcilePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode profile state reconcile task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	resource, exists := handler.planner.Resource(payload.ResourceTag)
	if !exists {
		return fmt.Errorf("profile state resource %q is not registered", payload.ResourceTag)
	}
	if resource.ProfileID != task.ProfileID {
		return errors.New("profile state reconcile task profile mismatch")
	}
	reader := handler.readers[resource.ProfileID]
	if reader == nil {
		return ErrProfileStateReconcileUnavailable
	}
	proposal, _, err := handler.planner.ReadAndPlan(ctx, resource.Tag, reader)
	if err != nil {
		return err
	}
	if proposal.Status == core.ProfileStateProposalNoChanges {
		return nil
	}
	_, _, err = handler.apply.Enqueue(ctx, proposal.ID, task.Source+":reconcile")
	return err
}
