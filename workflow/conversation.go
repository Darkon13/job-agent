package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

type ConversationObservationResult struct {
	Observed             int                   `json:"observed"`
	ConversationsCreated int                   `json:"conversations_created"`
	MessagesCreated      int                   `json:"messages_created"`
	ConversationIDs      []core.ConversationID `json:"conversation_ids"`
}

func (workflow *ConversationWorkflow) ObserveConversations(ctx context.Context, platform core.Platform, profileID core.ProfileID, observations []core.ConversationObservation, observedAt time.Time) (ConversationObservationResult, error) {
	if platform == "" || profileID == "" || observedAt.IsZero() {
		return ConversationObservationResult{}, errors.New("conversation catalog observation requires platform, profile and time")
	}
	result := ConversationObservationResult{
		Observed: len(observations), ConversationIDs: make([]core.ConversationID, 0, len(observations)),
	}
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return result, fmt.Errorf("conversation observation %d: %w", index, err)
		}
		conversationID, err := workflow.ids.NewID("conversation")
		if err != nil {
			return result, err
		}
		candidate, err := core.NewConversation(core.ConversationID(conversationID), platform, profileID, observation.ExternalID, observedAt)
		if err != nil {
			return result, err
		}
		stored, created, err := workflow.repository.CreateConversation(ctx, candidate)
		if err != nil {
			return result, fmt.Errorf("store observed conversation %s: %w", observation.ExternalID, err)
		}
		if created {
			result.ConversationsCreated++
		}
		// A full history sync is only needed when the catalog shows activity:
		// a new conversation, a changed unread/status state or a new last
		// message. Unchanged conversations stay on their stored timeline.
		needsSync := created
		if !created {
			changed, err := workflow.updateConversation(ctx, stored.ID, func(conversation *core.Conversation) (bool, error) {
				changed, observeErr := conversation.ObserveCatalogState(observation.Status, observation.UnreadCount, observedAt)
				if errors.Is(observeErr, core.ErrConversationObservationStale) {
					// A concurrent sync advanced the conversation after the catalog
					// snapshot; its state is newer and must not be overwritten.
					return false, nil
				}
				return changed, observeErr
			})
			if err != nil {
				return result, fmt.Errorf("observe conversation %s: %w", observation.ExternalID, err)
			}
			needsSync = needsSync || changed
		}
		if observation.Status != core.ConversationActive {
			// The platform closed the chat, which is how answered questionnaires
			// end; pending reminders are not meaningful any more.
			if err := workflow.cancelPendingFollowUps(ctx, stored.ID); err != nil {
				return result, fmt.Errorf("cancel follow-ups for %s: %w", observation.ExternalID, err)
			}
		}
		if observation.LastMessage != nil {
			messageID, err := workflow.ids.NewID("message")
			if err != nil {
				return result, err
			}
			message, err := observation.LastMessage.Message(core.MessageID(messageID), stored.ID)
			if err != nil {
				return result, err
			}
			_, messageCreated, appendErr := workflow.repository.AppendConversationMessage(ctx, message, observedAt)
			switch {
			case appendErr == nil && messageCreated:
				result.MessagesCreated++
				needsSync = true
			case appendErr == nil:
			case errors.Is(appendErr, storage.ErrConversationMessageConflict):
				// The platform may edit a message or re-parse it differently; the
				// sync must not fail because one identity changed its content.
			case errors.Is(appendErr, core.ErrConversationObservationStale):
				// A concurrent sync advanced the conversation past the catalog
				// snapshot; the full sync will settle the last message.
				needsSync = true
			default:
				return result, fmt.Errorf("store observed conversation message %s: %w", observation.LastMessage.ExternalID, appendErr)
			}
		}
		if needsSync {
			result.ConversationIDs = append(result.ConversationIDs, stored.ID)
		}
	}
	return result, nil
}

// cancelPendingFollowUps removes scheduled reminders of a chat the platform
// closed. A later observation of an active conversation may schedule new ones.
func (workflow *ConversationWorkflow) cancelPendingFollowUps(ctx context.Context, conversationID core.ConversationID) error {
	followUps, err := workflow.repository.ListFollowUps(ctx, storage.FollowUpFilter{ConversationID: conversationID})
	if err != nil {
		return err
	}
	for _, followUp := range followUps {
		if followUp.Status != core.FollowUpScheduled && followUp.Status != core.FollowUpQueued {
			continue
		}
		if _, err := workflow.CancelFollowUp(ctx, followUp.ID, core.FollowUpConversationInactive); err != nil {
			return err
		}
	}
	return nil
}

func (workflow *ConversationWorkflow) EnqueueConversationSync(ctx context.Context, conversationID core.ConversationID, requestKey string, priority core.TaskPriority) (bool, error) {
	conversation, err := workflow.repository.Conversation(ctx, conversationID)
	if err != nil {
		return false, fmt.Errorf("load conversation for sync: %w", err)
	}
	idempotencyKey, err := core.ConversationSyncIdempotencyKey(conversation.ID, requestKey)
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(core.ConversationIDPayload{ConversationID: conversation.ID})
	if err != nil {
		return false, fmt.Errorf("encode conversation sync task: %w", err)
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
		ID: core.TaskID(taskID), Type: core.TaskConversationSync,
		IdempotencyKey: idempotencyKey, Source: "conversation-discovery",
		Platform: conversation.Platform, ProfileID: conversation.ProfileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
		Priority: priority, AvailableAt: workflow.clock.Now(),
	}, workflow.clock.Now())
	if err != nil {
		return false, err
	}
	created, err := workflow.tasks.Enqueue(ctx, task)
	if err != nil {
		return false, fmt.Errorf("enqueue conversation sync %s: %w", conversation.ID, err)
	}
	return created, nil
}

func (workflow *ConversationWorkflow) ObserveConversationPresentation(ctx context.Context, conversationID core.ConversationID, presentation core.ConversationPresentation, observedAt time.Time) error {
	_, err := workflow.updateConversation(ctx, conversationID, func(conversation *core.Conversation) (bool, error) {
		// Platform message timestamps may run slightly ahead of the local
		// clock; presentation metadata is auxiliary and must not fail the
		// whole sync, so clamp the observation to the stored state.
		effective := observedAt
		if effective.Before(conversation.UpdatedAt) {
			effective = conversation.UpdatedAt
		}
		return conversation.ObservePresentation(presentation, effective)
	})
	if err != nil {
		return fmt.Errorf("save conversation presentation: %w", err)
	}
	return nil
}

// LinkConversationApplication records which application a chat belongs to so a
// removal can delete the chat together with its application.
func (workflow *ConversationWorkflow) LinkConversationApplication(ctx context.Context, conversationID core.ConversationID, applicationID core.ApplicationID) error {
	if strings.TrimSpace(string(applicationID)) == "" {
		return nil
	}
	if _, err := workflow.repository.AttachConversationApplication(ctx, conversationID, applicationID, workflow.clock.Now()); err != nil {
		return fmt.Errorf("link conversation to application: %w", err)
	}
	return nil
}

func (workflow *ConversationWorkflow) MarkConversationRead(ctx context.Context, conversationID core.ConversationID) error {
	_, err := workflow.updateConversation(ctx, conversationID, func(conversation *core.Conversation) (bool, error) {
		return conversation.MarkRead(workflow.clock.Now())
	})
	if err != nil {
		return fmt.Errorf("save conversation read state: %w", err)
	}
	return nil
}

const conversationUpdateAttempts = 5

// updateConversation applies one optimistic read-modify-write step. Discovery
// and per-chat syncs can revise the same conversation concurrently, so a
// revision conflict is retried against the fresh revision instead of failing
// the whole catalog observation.
// CloseConversationDueToPlatform marks the chat closed after the platform
// refused a write. A later catalog sync reopens it if the platform shows the
// conversation active again.
func (workflow *ConversationWorkflow) CloseConversationDueToPlatform(ctx context.Context, conversationID core.ConversationID) error {
	_, err := workflow.updateConversation(ctx, conversationID, func(conversation *core.Conversation) (bool, error) {
		if conversation.Status == core.ConversationClosed {
			return false, nil
		}
		now := workflow.clock.Now()
		if now.Before(conversation.UpdatedAt) {
			now = conversation.UpdatedAt
		}
		if err := conversation.SetStatus(core.ConversationClosed, now); err != nil {
			return false, err
		}
		return true, nil
	})
	return err
}

func (workflow *ConversationWorkflow) updateConversation(ctx context.Context, conversationID core.ConversationID, mutate func(*core.Conversation) (bool, error)) (bool, error) {
	for attempt := 0; attempt < conversationUpdateAttempts; attempt++ {
		conversation, err := workflow.repository.Conversation(ctx, conversationID)
		if err != nil {
			return false, err
		}
		expectedRevision := conversation.Revision
		changed, err := mutate(&conversation)
		if err != nil {
			return false, err
		}
		if !changed {
			return false, nil
		}
		err = workflow.repository.SaveConversation(ctx, conversation, expectedRevision)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, storage.ErrRevisionConflict) {
			return false, err
		}
	}
	return false, &core.OperationError{
		Category: core.ErrorTemporaryFailure, Operation: "conversations.update",
		Message: fmt.Sprintf("conversation %s was revised concurrently", conversationID),
	}
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
