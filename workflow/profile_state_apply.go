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

var (
	ErrProfileStateNoChanges         = errors.New("profile state proposal has no changes")
	ErrProfileStateWriterUnavailable = errors.New("profile state writer is unavailable")
)

type ProfileStateApplyWorkflow struct {
	proposals storage.ProfileStateProposalRepository
	tasks     broker.TaskStore
	clock     Clock
	ids       IDGenerator
	platforms map[core.ProfileID]core.Platform
}

func NewProfileStateApplyWorkflow(proposals storage.ProfileStateProposalRepository, tasks broker.TaskStore, clock Clock, ids IDGenerator, platforms map[core.ProfileID]core.Platform) (*ProfileStateApplyWorkflow, error) {
	if proposals == nil || tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("profile state apply workflow requires proposals, task store, clock and id generator")
	}
	copiedPlatforms := make(map[core.ProfileID]core.Platform, len(platforms))
	for profileID, platform := range platforms {
		if profileID == "" || platform == "" {
			return nil, errors.New("profile state apply workflow platforms require profile and platform")
		}
		copiedPlatforms[profileID] = platform
	}
	return &ProfileStateApplyWorkflow{proposals: proposals, tasks: tasks, clock: clock, ids: ids, platforms: copiedPlatforms}, nil
}

func (workflow *ProfileStateApplyWorkflow) Writable(profileID core.ProfileID) bool {
	if workflow == nil {
		return false
	}
	_, exists := workflow.platforms[profileID]
	return exists
}

func (workflow *ProfileStateApplyWorkflow) Enqueue(ctx context.Context, proposalID core.ProfileStateProposalID, source string) (core.Task, bool, error) {
	if workflow == nil {
		return core.Task{}, false, errors.New("profile state apply workflow is nil")
	}
	if proposalID == "" || strings.TrimSpace(source) == "" {
		return core.Task{}, false, errors.New("profile state apply requires proposal and source")
	}
	proposal, err := workflow.proposals.ProfileStateProposal(ctx, proposalID)
	if err != nil {
		return core.Task{}, false, err
	}
	if proposal.Status == core.ProfileStateProposalNoChanges {
		return core.Task{}, false, ErrProfileStateNoChanges
	}
	platform, writable := workflow.platforms[proposal.ProfileID]
	if !writable {
		return core.Task{}, false, ErrProfileStateWriterUnavailable
	}
	payload, err := json.Marshal(core.ProfileStateApplyPayload{ProposalID: proposal.ID})
	if err != nil {
		return core.Task{}, false, fmt.Errorf("encode profile state apply task: %w", err)
	}
	idempotencyKey, err := core.ProfileStateApplyIdempotencyKey(proposal)
	if err != nil {
		return core.Task{}, false, err
	}
	id, err := workflow.ids.NewID("task")
	if err != nil {
		return core.Task{}, false, err
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(id), Type: core.TaskProfileStateApply, IdempotencyKey: idempotencyKey,
		Source: strings.TrimSpace(source), Platform: platform, ProfileID: proposal.ProfileID,
		CorrelationID: core.CorrelationID(proposal.ID), Payload: payload,
		Priority: core.TaskPriorityProfileStateApply,
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
