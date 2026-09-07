package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/workflow"
)

type ConversationFollowUpSelectionHandler struct {
	workflow *workflow.ConversationWorkflow
}

func NewConversationFollowUpSelectionHandler(conversationWorkflow *workflow.ConversationWorkflow) (*ConversationFollowUpSelectionHandler, error) {
	if conversationWorkflow == nil {
		return nil, errors.New("conversation follow-up selection handler requires workflow")
	}
	return &ConversationFollowUpSelectionHandler{workflow: conversationWorkflow}, nil
}

func (handler *ConversationFollowUpSelectionHandler) Handle(ctx context.Context, task core.Task) error {
	if task.Type != core.TaskConversationFollowUpSelect {
		return errors.New("conversation follow-up selection task has invalid type")
	}
	var payload core.ConversationFollowUpSelectPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode conversation follow-up selection task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if task.ProfileID != payload.ProfileID {
		return errors.New("conversation follow-up selection task identity mismatch")
	}
	_, err := handler.workflow.SelectAndScheduleFollowUp(ctx, workflow.SelectFollowUpRequest{
		ProfileID: payload.ProfileID, Strategy: payload.Strategy,
		MinimumSilence: payload.MinimumSilence.Value(), RunAfter: payload.RunAfter.Value(),
		DeadlineAfter: payload.DeadlineAfter.Value(), Content: payload.Content, Policy: payload.Policy,
		IdempotencyKey: task.IdempotencyKey,
	})
	return err
}
