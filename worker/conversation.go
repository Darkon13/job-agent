package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

type ConversationTransportRegistry struct {
	mu         sync.RWMutex
	transports map[core.ProfileID]adapter.ConversationTransport
}

func NewConversationTransportRegistry() *ConversationTransportRegistry {
	return &ConversationTransportRegistry{transports: make(map[core.ProfileID]adapter.ConversationTransport)}
}

func (registry *ConversationTransportRegistry) Register(profileID core.ProfileID, transport adapter.ConversationTransport) error {
	if profileID == "" || transport == nil {
		return errors.New("conversation transport registration requires profile and transport")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.transports[profileID]; exists {
		return fmt.Errorf("conversation transport for profile %s is already registered", profileID)
	}
	registry.transports[profileID] = transport
	return nil
}

func (registry *ConversationTransportRegistry) Resolve(profileID core.ProfileID) (adapter.ConversationTransport, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	transport, exists := registry.transports[profileID]
	if !exists {
		return nil, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "conversations.route",
			Message: fmt.Sprintf("no conversation transport for profile %s", profileID),
		}
	}
	return transport, nil
}

type MessageResolver interface {
	Resolve(ctx context.Context, conversation core.Conversation, content core.MessageContent) (string, error)
}

type StaticMessageResolver struct{}

func (StaticMessageResolver) Resolve(_ context.Context, _ core.Conversation, content core.MessageContent) (string, error) {
	if err := content.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(content.Text) == "" {
		return "", &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "conversation.render",
			Message: "template and operator message resolution is not implemented",
		}
	}
	return content.Text, nil
}

type ConversationHandlers struct {
	repository storage.ConversationRepository
	activity   storage.ProfileActivityRepository
	workflow   *workflow.ConversationWorkflow
	transports *ConversationTransportRegistry
	resolver   MessageResolver
	clock      Clock
}

func NewConversationHandlers(repository storage.ConversationRepository, activity storage.ProfileActivityRepository, conversationWorkflow *workflow.ConversationWorkflow, transports *ConversationTransportRegistry, resolver MessageResolver, clock Clock) (*ConversationHandlers, error) {
	if repository == nil || activity == nil || conversationWorkflow == nil || transports == nil || resolver == nil || clock == nil {
		return nil, errors.New("conversation handlers require all dependencies")
	}
	return &ConversationHandlers{repository: repository, activity: activity, workflow: conversationWorkflow, transports: transports, resolver: resolver, clock: clock}, nil
}

func (handlers *ConversationHandlers) Send(ctx context.Context, task core.Task) error {
	var payload core.ConversationSendPayload
	if err := decodeTaskPayload(task, &payload); err != nil {
		return err
	}
	conversation, err := handlers.repository.Conversation(ctx, payload.ConversationID)
	if err != nil {
		return err
	}
	text, err := handlers.resolver.Resolve(ctx, conversation, payload.Content)
	if err != nil {
		return err
	}
	return handlers.send(ctx, task, conversation, payload.ReplyToID, text)
}

func (handlers *ConversationHandlers) FollowUp(ctx context.Context, task core.Task) error {
	var payload core.ConversationFollowUpPayload
	if err := decodeTaskPayload(task, &payload); err != nil {
		return err
	}
	followUp, outcome, err := handlers.workflow.PrepareFollowUp(ctx, payload.FollowUpID)
	if err != nil {
		return err
	}
	if outcome != workflow.FollowUpReady {
		return nil
	}
	conversation, err := handlers.repository.Conversation(ctx, followUp.ConversationID)
	if err != nil {
		return err
	}
	text, err := handlers.resolver.Resolve(ctx, conversation, followUp.Content)
	if err != nil {
		return err
	}
	message, err := handlers.sendMessage(ctx, task, conversation, followUp.AnchorMessageID, text)
	if err != nil {
		return err
	}
	_, err = handlers.workflow.MarkFollowUpSent(ctx, followUp.ID, message.ID)
	return err
}

func (handlers *ConversationHandlers) MarkRead(ctx context.Context, task core.Task) error {
	var payload core.ConversationIDPayload
	if err := decodeTaskPayload(task, &payload); err != nil {
		return err
	}
	conversation, err := handlers.repository.Conversation(ctx, payload.ConversationID)
	if err != nil {
		return err
	}
	transport, err := handlers.transports.Resolve(conversation.ProfileID)
	if err != nil {
		return err
	}
	return transport.MarkConversationRead(ctx, conversation.ProfileID, conversation.ExternalID)
}

func (handlers *ConversationHandlers) Sync(ctx context.Context, task core.Task) error {
	var payload core.ConversationIDPayload
	if err := decodeTaskPayload(task, &payload); err != nil {
		return err
	}
	conversation, err := handlers.repository.Conversation(ctx, payload.ConversationID)
	if err != nil {
		return err
	}
	transport, err := handlers.transports.Resolve(conversation.ProfileID)
	if err != nil {
		return err
	}
	result, err := transport.SyncConversation(ctx, conversation.ProfileID, conversation.ID, conversation.ExternalID)
	if err != nil {
		return err
	}
	if result.ObservedAt.IsZero() {
		return errors.New("conversation sync returned zero observation time")
	}
	for _, message := range result.Messages {
		if _, _, err := handlers.repository.AppendConversationMessage(ctx, message, result.ObservedAt); err != nil {
			return err
		}
	}
	return nil
}

func (handlers *ConversationHandlers) send(ctx context.Context, task core.Task, conversation core.Conversation, replyToID core.MessageID, text string) error {
	_, err := handlers.sendMessage(ctx, task, conversation, replyToID, text)
	return err
}

func (handlers *ConversationHandlers) sendMessage(ctx context.Context, task core.Task, conversation core.Conversation, replyToID core.MessageID, text string) (core.ConversationMessage, error) {
	transport, err := handlers.transports.Resolve(conversation.ProfileID)
	if err != nil {
		return core.ConversationMessage{}, err
	}
	message, err := transport.SendConversationMessage(ctx, adapter.ConversationSendCommand{
		ProfileID: conversation.ProfileID, ConversationID: conversation.ID,
		ExternalConversationID: conversation.ExternalID, ReplyToID: replyToID,
		Text: text, IdempotencyKey: task.IdempotencyKey,
	})
	if err != nil {
		return core.ConversationMessage{}, err
	}
	if message.ConversationID != conversation.ID || message.Direction != core.MessageOutgoing || message.Status != core.MessageSent {
		return core.ConversationMessage{}, errors.New("conversation transport returned invalid outgoing message identity or state")
	}
	if _, _, err := handlers.repository.AppendConversationMessage(ctx, message, handlers.clock.Now()); err != nil {
		return core.ConversationMessage{}, err
	}
	if err := recordProfileActivity(ctx, handlers.activity, conversation.Platform, conversation.ProfileID, "",
		core.ProfileActivityConversationMessageSent, string(message.ID), message.OccurredAt); err != nil {
		return core.ConversationMessage{}, err
	}
	return message, nil
}

func decodeTaskPayload(task core.Task, target any) error {
	if len(task.Payload) == 0 || !json.Valid(task.Payload) {
		return errors.New("conversation task contains invalid JSON payload")
	}
	decoder := json.NewDecoder(strings.NewReader(string(task.Payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode conversation task %s: %w", task.Type, err)
	}
	return nil
}
