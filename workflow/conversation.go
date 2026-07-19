package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type ConversationWorkflow struct {
	repository storage.ConversationRepository
	tasks      broker.TaskStore
	clock      Clock
	ids        IDGenerator
}

func NewConversationWorkflow(repository storage.ConversationRepository, tasks broker.TaskStore, clock Clock, ids IDGenerator) (*ConversationWorkflow, error) {
	if repository == nil || tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("conversation workflow requires repository, task queue, clock and id generator")
	}
	return &ConversationWorkflow{repository: repository, tasks: tasks, clock: clock, ids: ids}, nil
}

type ScheduleFollowUpRequest struct {
	ConversationID  core.ConversationID
	AnchorMessageID core.MessageID
	AnchorAt        time.Time
	RunAt           time.Time
	Deadline        *time.Time
	Content         core.MessageContent
	Policy          core.FollowUpPolicy
	IdempotencyKey  string
}

func (workflow *ConversationWorkflow) ScheduleFollowUp(ctx context.Context, request ScheduleFollowUpRequest) (core.FollowUp, bool, error) {
	conversation, err := workflow.repository.Conversation(ctx, request.ConversationID)
	if err != nil {
		return core.FollowUp{}, false, fmt.Errorf("load follow-up conversation: %w", err)
	}
	now := workflow.clock.Now()
	idempotencyKey, err := core.ConversationFollowUpRequestIdempotencyKey(request.ConversationID, request.IdempotencyKey)
	if err != nil {
		return core.FollowUp{}, false, err
	}
	id, err := workflow.ids.NewID("follow-up")
	if err != nil {
		return core.FollowUp{}, false, err
	}
	candidate, err := core.NewFollowUp(core.NewFollowUpParams{
		ID: core.FollowUpID(id), ConversationID: conversation.ID, ProfileID: conversation.ProfileID,
		Platform: conversation.Platform, AnchorMessageID: request.AnchorMessageID, AnchorAt: request.AnchorAt,
		RunAt: request.RunAt, Deadline: request.Deadline, Content: request.Content,
		Policy: request.Policy, IdempotencyKey: idempotencyKey,
	}, now)
	if err != nil {
		return core.FollowUp{}, false, err
	}
	if reason, cancel, err := candidate.CancellationFor(conversation); err != nil {
		return core.FollowUp{}, false, err
	} else if cancel {
		return core.FollowUp{}, false, fmt.Errorf("follow-up is already invalid: %s", reason)
	}
	stored, created, err := workflow.repository.CreateFollowUp(ctx, candidate)
	if err != nil {
		return core.FollowUp{}, false, fmt.Errorf("store follow-up: %w", err)
	}
	return stored, created, nil
}

type FollowUpReconcileResult struct {
	Due          int `json:"due"`
	TasksCreated int `json:"tasks_created"`
}

func (workflow *ConversationWorkflow) ReconcileDueFollowUps(ctx context.Context, dueAt time.Time) (FollowUpReconcileResult, error) {
	if dueAt.IsZero() {
		return FollowUpReconcileResult{}, errors.New("follow-up reconciliation requires due time")
	}
	followUps, err := workflow.repository.ListFollowUps(ctx, storage.FollowUpFilter{
		Status: core.FollowUpScheduled, DueBefore: &dueAt,
	})
	if err != nil {
		return FollowUpReconcileResult{}, fmt.Errorf("list due follow-ups: %w", err)
	}
	result := FollowUpReconcileResult{Due: len(followUps)}
	for _, followUp := range followUps {
		created, err := workflow.enqueueFollowUp(ctx, followUp)
		if err != nil {
			return result, err
		}
		if created {
			result.TasksCreated++
		}
	}
	return result, nil
}

func (workflow *ConversationWorkflow) enqueueFollowUp(ctx context.Context, followUp core.FollowUp) (bool, error) {
	payload, err := json.Marshal(core.ConversationFollowUpPayload{FollowUpID: followUp.ID})
	if err != nil {
		return false, fmt.Errorf("encode follow-up task: %w", err)
	}
	idempotencyKey, err := core.ConversationFollowUpIdempotencyKey(followUp.ID)
	if err != nil {
		return false, err
	}
	taskID, err := workflow.ids.NewID("task")
	if err != nil {
		return false, err
	}
	correlationID, err := workflow.ids.NewID("correlation")
	if err != nil {
		return false, err
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(taskID), Type: core.TaskConversationFollowUp,
		IdempotencyKey: idempotencyKey, Source: "follow-up-scheduler",
		Platform: followUp.Platform, ProfileID: followUp.ProfileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
		AvailableAt: followUp.RunAt, Deadline: followUp.Deadline,
	}, workflow.clock.Now())
	if err != nil {
		return false, err
	}
	created, err := workflow.tasks.Enqueue(ctx, task)
	if err != nil {
		return false, fmt.Errorf("enqueue follow-up %s: %w", followUp.ID, err)
	}
	return created, nil
}

type FollowUpPreparation string

const (
	FollowUpReady     FollowUpPreparation = "ready"
	FollowUpCancelled FollowUpPreparation = "cancelled"
	FollowUpExpired   FollowUpPreparation = "expired"
	FollowUpTerminal  FollowUpPreparation = "terminal"
)

func (workflow *ConversationWorkflow) PrepareFollowUp(ctx context.Context, id core.FollowUpID) (core.FollowUp, FollowUpPreparation, error) {
	followUp, err := workflow.repository.FollowUp(ctx, id)
	if err != nil {
		return core.FollowUp{}, "", err
	}
	if followUp.Status != core.FollowUpScheduled && followUp.Status != core.FollowUpQueued {
		return followUp, FollowUpTerminal, nil
	}
	now := workflow.clock.Now()
	if followUp.Deadline != nil && !now.Before(*followUp.Deadline) {
		expectedRevision := followUp.Revision
		if err := followUp.Expire(now); err != nil {
			return core.FollowUp{}, "", err
		}
		if err := workflow.repository.SaveFollowUp(ctx, followUp, expectedRevision); err != nil {
			return core.FollowUp{}, "", err
		}
		return followUp, FollowUpExpired, nil
	}
	conversation, err := workflow.repository.Conversation(ctx, followUp.ConversationID)
	if err != nil {
		return core.FollowUp{}, "", err
	}
	if reason, cancel, err := followUp.CancellationFor(conversation); err != nil {
		return core.FollowUp{}, "", err
	} else if cancel {
		return workflow.cancelPrepared(ctx, followUp, reason, now)
	}
	sent, err := workflow.repository.ListFollowUps(ctx, storage.FollowUpFilter{
		ConversationID: followUp.ConversationID, Status: core.FollowUpSent,
	})
	if err != nil {
		return core.FollowUp{}, "", err
	}
	if len(sent) >= followUp.Policy.MaxFollowUps {
		return workflow.cancelPrepared(ctx, followUp, core.FollowUpPolicyDenied, now)
	}
	if conversation.LastOutgoingAt != nil && followUp.Policy.Cooldown > 0 &&
		now.Before(conversation.LastOutgoingAt.Add(followUp.Policy.Cooldown.Value())) {
		return workflow.cancelPrepared(ctx, followUp, core.FollowUpPolicyDenied, now)
	}
	if followUp.Status == core.FollowUpScheduled {
		expectedRevision := followUp.Revision
		if err := followUp.Queue(now); err != nil {
			return core.FollowUp{}, "", err
		}
		if err := workflow.repository.SaveFollowUp(ctx, followUp, expectedRevision); err != nil {
			return core.FollowUp{}, "", err
		}
	}
	return followUp, FollowUpReady, nil
}

func (workflow *ConversationWorkflow) cancelPrepared(ctx context.Context, followUp core.FollowUp, reason core.FollowUpCancelReason, now time.Time) (core.FollowUp, FollowUpPreparation, error) {
	expectedRevision := followUp.Revision
	if err := followUp.Cancel(reason, now); err != nil {
		return core.FollowUp{}, "", err
	}
	if err := workflow.repository.SaveFollowUp(ctx, followUp, expectedRevision); err != nil {
		return core.FollowUp{}, "", err
	}
	return followUp, FollowUpCancelled, nil
}

func (workflow *ConversationWorkflow) MarkFollowUpSent(ctx context.Context, id core.FollowUpID, messageID core.MessageID) (core.FollowUp, error) {
	followUp, err := workflow.repository.FollowUp(ctx, id)
	if err != nil {
		return core.FollowUp{}, err
	}
	expectedRevision := followUp.Revision
	if err := followUp.MarkSent(messageID, workflow.clock.Now()); err != nil {
		return core.FollowUp{}, err
	}
	return followUp, workflow.repository.SaveFollowUp(ctx, followUp, expectedRevision)
}

func (workflow *ConversationWorkflow) CancelFollowUp(ctx context.Context, id core.FollowUpID, reason core.FollowUpCancelReason) (core.FollowUp, error) {
	followUp, err := workflow.repository.FollowUp(ctx, id)
	if err != nil {
		return core.FollowUp{}, err
	}
	if followUp.Status == core.FollowUpCancelled {
		return followUp, nil
	}
	if followUp.Status != core.FollowUpScheduled && followUp.Status != core.FollowUpQueued {
		return core.FollowUp{}, errors.New("terminal follow-up cannot be cancelled")
	}
	expectedRevision := followUp.Revision
	if err := followUp.Cancel(reason, workflow.clock.Now()); err != nil {
		return core.FollowUp{}, err
	}
	return followUp, workflow.repository.SaveFollowUp(ctx, followUp, expectedRevision)
}

func (workflow *ConversationWorkflow) RescheduleFollowUp(ctx context.Context, id core.FollowUpID, runAt time.Time, deadline *time.Time, expectedRevision uint64) (core.FollowUp, error) {
	followUp, err := workflow.repository.FollowUp(ctx, id)
	if err != nil {
		return core.FollowUp{}, err
	}
	if err := followUp.Reschedule(runAt, deadline, expectedRevision, workflow.clock.Now()); err != nil {
		return core.FollowUp{}, err
	}
	return followUp, workflow.repository.SaveFollowUp(ctx, followUp, expectedRevision)
}

func (workflow *ConversationWorkflow) RunFollowUpNow(ctx context.Context, id core.FollowUpID, expectedRevision uint64) (core.FollowUp, bool, error) {
	followUp, err := workflow.repository.FollowUp(ctx, id)
	if err != nil {
		return core.FollowUp{}, false, err
	}
	now := workflow.clock.Now()
	if err := followUp.Reschedule(now, followUp.Deadline, expectedRevision, now); err != nil {
		return core.FollowUp{}, false, err
	}
	if err := workflow.repository.SaveFollowUp(ctx, followUp, expectedRevision); err != nil {
		return core.FollowUp{}, false, err
	}
	result, err := workflow.ReconcileDueFollowUps(ctx, now)
	if err != nil {
		return core.FollowUp{}, false, err
	}
	return followUp, result.TasksCreated > 0, nil
}

func (workflow *ConversationWorkflow) EnqueueMessage(ctx context.Context, conversationID core.ConversationID, content core.MessageContent, replyToID core.MessageID, requestKey string) (core.Task, bool, error) {
	payload := core.ConversationSendPayload{ConversationID: conversationID, ReplyToID: replyToID, Content: content}
	if err := payload.Validate(); err != nil {
		return core.Task{}, false, err
	}
	return workflow.enqueueConversationCommand(ctx, conversationID, core.TaskConversationSend, payload, requestKey)
}

func (workflow *ConversationWorkflow) EnqueueMarkRead(ctx context.Context, conversationID core.ConversationID, requestKey string) (core.Task, bool, error) {
	return workflow.enqueueConversationCommand(ctx, conversationID, core.TaskConversationMarkRead, core.ConversationIDPayload{ConversationID: conversationID}, requestKey)
}

func (workflow *ConversationWorkflow) EnqueueSync(ctx context.Context, conversationID core.ConversationID, requestKey string) (core.Task, bool, error) {
	return workflow.enqueueConversationCommand(ctx, conversationID, core.TaskConversationSync, core.ConversationIDPayload{ConversationID: conversationID}, requestKey)
}

func (workflow *ConversationWorkflow) enqueueConversationCommand(ctx context.Context, conversationID core.ConversationID, taskType core.TaskType, payload any, requestKey string) (core.Task, bool, error) {
	conversation, err := workflow.repository.Conversation(ctx, conversationID)
	if err != nil {
		return core.Task{}, false, err
	}
	idempotencyKey, err := core.ConversationSendIdempotencyKey(conversationID, string(taskType)+"\x00"+requestKey)
	if err != nil {
		return core.Task{}, false, err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return core.Task{}, false, err
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
		ID: core.TaskID(taskID), Type: taskType, IdempotencyKey: idempotencyKey,
		Source: "conversation-api", Platform: conversation.Platform, ProfileID: conversation.ProfileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payloadJSON,
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
		return core.Task{}, false, fmt.Errorf("load idempotent conversation task: %w", err)
	}
	return existing, false, nil
}
