package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

// ResumeUpdatePlanner reads the declared resource and builds an immutable
// proposal. *workflow.ProfileStatePlanner implements it.
type ResumeUpdatePlanner interface {
	Resource(tag string) (core.ProfileStateResource, bool)
	ReadAndPlan(ctx context.Context, resourceTag string, reader adapter.ProfileStateReader) (core.ProfileStateProposal, bool, error)
}

// ResumeUpdateHandler applies one declared resume resource and optionally
// publishes the resume after the writer confirmed the change with read-back.
// A no-change plan is a no-op and never triggers publish.
type ResumeUpdateHandler struct {
	planner    ResumeUpdatePlanner
	readers    map[core.ProfileID]adapter.ProfileStateReader
	writers    *ProfileStateWriterRegistry
	publishers *ResumePublisherRegistry
}

func NewResumeUpdateHandler(planner ResumeUpdatePlanner, readers map[core.ProfileID]adapter.ProfileStateReader, writers *ProfileStateWriterRegistry, publishers *ResumePublisherRegistry) (*ResumeUpdateHandler, error) {
	if planner == nil || writers == nil || publishers == nil {
		return nil, errors.New("resume update handler requires planner, writers and publishers")
	}
	return &ResumeUpdateHandler{planner: planner, readers: readers, writers: writers, publishers: publishers}, nil
}

func (handler *ResumeUpdateHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ResumeUpdatePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode resume update task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("resume update task profile mismatch")
	}
	resource, exists := handler.planner.Resource(payload.ResourceTag)
	if !exists {
		return resumeUpdateError("resume update resource is not declared")
	}
	if resource.ProfileID != payload.ProfileID {
		return resumeUpdateError("resume update resource belongs to another profile")
	}
	reader := handler.readers[payload.ProfileID]
	if reader == nil {
		return &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "resume.update",
			Message: "profile has no profile state reader",
		}
	}
	proposal, _, err := handler.planner.ReadAndPlan(ctx, payload.ResourceTag, reader)
	if err != nil {
		return err
	}
	if proposal.Status == core.ProfileStateProposalPlanned {
		writer, err := handler.writers.Resolve(payload.ProfileID)
		if err != nil {
			return err
		}
		if _, err := writer.ApplyProfileState(ctx, proposal); err != nil {
			return err
		}
	}
	if !payload.Publish {
		return nil
	}
	publisher, err := handler.publishers.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	_, err = publisher.PublishResume(ctx, adapter.ResumePublishCommand{
		ProfileID: payload.ProfileID, ResumeID: payload.ResumeID,
		IdempotencyKey: task.IdempotencyKey + ":publish",
	})
	return err
}

func resumeUpdateError(message string) error {
	return &core.OperationError{Category: core.ErrorValidationRequired, Operation: "resume.update", Message: message}
}
