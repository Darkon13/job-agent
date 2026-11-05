package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ConversationReplyGenerator produces one free-text answer to a recruiter
// question. It receives the observed conversation, the exact prompt and a
// bounded local history so the answer can stay grounded in profile facts.
type ConversationReplyGenerator func(context.Context, core.Conversation, core.ConversationMessage, []core.ConversationMessage) (string, error)

// ConversationReplyRegistry holds opt-in generators per profile. Config reload
// may add profiles while reply workers keep resolving through the registry.
type ConversationReplyRegistry struct {
	mu         sync.RWMutex
	generators map[core.ProfileID]ConversationReplyGenerator
}

func NewConversationReplyRegistry() *ConversationReplyRegistry {
	return &ConversationReplyRegistry{generators: make(map[core.ProfileID]ConversationReplyGenerator)}
}

func (registry *ConversationReplyRegistry) Register(profileID core.ProfileID, generator ConversationReplyGenerator) error {
	if profileID == "" || generator == nil {
		return errors.New("conversation reply registration requires profile and generator")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.generators[profileID]; exists {
		return fmt.Errorf("conversation reply generator for profile %s is already registered", profileID)
	}
	registry.generators[profileID] = generator
	return nil
}

func (registry *ConversationReplyRegistry) Resolve(profileID core.ProfileID) (ConversationReplyGenerator, bool) {
	if registry == nil {
		return nil, false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	generator, exists := registry.generators[profileID]
	return generator, exists
}

func (registry *ConversationReplyRegistry) Has(profileID core.ProfileID) bool {
	_, exists := registry.Resolve(profileID)
	return exists
}

func (registry *ConversationReplyRegistry) Len() int {
	if registry == nil {
		return 0
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.generators)
}

const (
	conversationReplyHistoryMessages = 8
	conversationReplyHistoryRunes    = 500
	conversationReplyMaximumRunes    = 600
	// Do not revive an ancient unanswered prompt when a profile enables
	// auto-replies: a question from a week ago may belong to a closed process.
	conversationReplyMaximumAge = 7 * 24 * time.Hour
)

// latestUnansweredIncoming returns the newest incoming message that still has
// no outgoing answer after it. A later system event or acknowledgement is the
// answer boundary: replying to an older question after the employer moved on
// would be worse than leaving it to a human.
func latestUnansweredIncoming(messages []core.ConversationMessage) (core.ConversationMessage, bool) {
	var lastOutgoingAt time.Time
	for _, message := range messages {
		if message.Direction != core.MessageOutgoing {
			continue
		}
		if message.Status != core.MessageSent && message.Status != core.MessageQueued {
			continue
		}
		if message.OccurredAt.After(lastOutgoingAt) {
			lastOutgoingAt = message.OccurredAt
		}
	}
	var latest core.ConversationMessage
	found := false
	for _, message := range messages {
		if message.Direction != core.MessageIncoming || !message.OccurredAt.After(lastOutgoingAt) {
			continue
		}
		if !found || message.OccurredAt.After(latest.OccurredAt) ||
			(message.OccurredAt.Equal(latest.OccurredAt) && message.ID > latest.ID) {
			latest = message
			found = true
		}
	}
	return latest, found
}

func replyableIncoming(message core.ConversationMessage) bool {
	switch message.Kind {
	case core.MessageText:
		return strings.TrimSpace(message.Text) != ""
	case core.MessageQuestionnaire:
		// A button questionnaire stays with the reviewed answer resolver; only
		// free-text prompts (HH AI questions) are eligible for generation.
		return len(message.Options) == 0 && strings.TrimSpace(message.Text) != ""
	default:
		return false
	}
}

func replyablePrompt(prompt core.ConversationMessage, now time.Time) bool {
	if !replyableIncoming(prompt) {
		return false
	}
	return !prompt.OccurredAt.Before(now.Add(-conversationReplyMaximumAge))
}

func conversationPromptKey(message core.ConversationMessage) string {
	if external := strings.TrimSpace(message.ExternalID); external != "" {
		return external
	}
	return string(message.ID)
}

func conversationReplyHistory(messages []core.ConversationMessage, prompt core.ConversationMessage) []core.ConversationMessage {
	history := make([]core.ConversationMessage, 0, conversationReplyHistoryMessages)
	for index := len(messages) - 1; index >= 0 && len(history) < conversationReplyHistoryMessages; index-- {
		message := messages[index]
		if message.ID == prompt.ID {
			continue
		}
		if message.OccurredAt.After(prompt.OccurredAt) {
			continue
		}
		if message.Kind == core.MessageSystem || strings.TrimSpace(message.Text) == "" {
			continue
		}
		history = append(history, message)
	}
	for left, right := 0, len(history)-1; left < right; left, right = left+1, right-1 {
		history[left], history[right] = history[right], history[left]
	}
	for index := range history {
		history[index].Text = truncateReplyRunes(history[index].Text, conversationReplyHistoryRunes)
	}
	return history
}

// normalizeAutoReply is the final local gate before an answer leaves the
// process. Empty or service-like model output is treated as no answer, not as
// a reason to retry the prompt forever.
func normalizeAutoReply(text string) (string, bool) {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "\"'«»")
	if text == "" || strings.EqualFold(text, "unknown") || strings.EqualFold(text, "нет ответа") {
		return "", false
	}
	if strings.Contains(text, "{{") || strings.Contains(text, "}}") ||
		strings.Contains(text, "```") || strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		return "", false
	}
	// Auto-replies must not leak the profile's contacts or external links that
	// the model may have copied from the facts.
	if strings.Contains(text, "@") || strings.Contains(text, "http://") ||
		strings.Contains(text, "https://") || strings.Contains(text, "t.me/") {
		return "", false
	}
	if utf8.RuneCountInString(text) > conversationReplyMaximumRunes {
		return "", false
	}
	if strings.ContainsAny(text, "\x00\r") {
		return "", false
	}
	return text, true
}

func truncateReplyRunes(text string, maximum int) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= maximum {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:maximum]))
}

func (handlers *ConversationHandlers) ConfigureAutoReplies(registry *ConversationReplyRegistry) {
	if handlers == nil {
		return
	}
	handlers.autoReplies = registry
}

// enqueueAutoReply creates at most one durable reply task per observed prompt.
// Generation and sending stay in the task handler so a slow model call never
// blocks conversation sync.
func (handlers *ConversationHandlers) enqueueAutoReply(ctx context.Context, conversation core.Conversation) error {
	if handlers.autoReplies == nil || !handlers.autoReplies.Has(conversation.ProfileID) {
		return nil
	}
	fresh, err := handlers.repository.Conversation(ctx, conversation.ID)
	if err != nil {
		if errors.Is(err, storage.ErrConversationNotFound) {
			return nil
		}
		return err
	}
	if fresh.Status != core.ConversationActive {
		return nil
	}
	messages, err := handlers.repository.ConversationMessages(ctx, fresh.ID)
	if err != nil {
		return err
	}
	prompt, found := latestUnansweredIncoming(messages)
	if !found || !replyablePrompt(prompt, handlers.clock.Now()) {
		return nil
	}
	decision := DecideChatReply(prompt.Text)
	if !decision.Reply {
		return nil
	}
	payload := core.ConversationReplyPayload{
		ConversationID: fresh.ID, PromptMessageID: prompt.ID, PromptExternalID: prompt.ExternalID,
	}
	_, _, err = handlers.workflow.EnqueueReply(ctx, payload, conversationPromptKey(prompt))
	return err
}

// Reply generates and sends one automatic answer to an observed question. It
// re-reads the chat and repeats every guard so a prompt answered by a human or
// superseded by a newer message never receives a second reply.
func (handlers *ConversationHandlers) Reply(ctx context.Context, task core.Task) error {
	var payload core.ConversationReplyPayload
	if err := decodeTaskPayload(task, &payload); err != nil {
		return err
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	conversation, found, err := handlers.loadConversation(ctx, payload.ConversationID)
	if err != nil {
		return err
	}
	if !found || conversation.Status != core.ConversationActive {
		return nil
	}
	generator, available := handlers.autoReplies.Resolve(conversation.ProfileID)
	if !available {
		return nil
	}
	// A human may have answered from the platform UI after the reply task was
	// enqueued. Refresh before generating so the automatic answer never
	// duplicates a message the account already sent.
	if err := handlers.syncConversation(ctx, conversation, false); err != nil {
		return err
	}
	conversation, found, err = handlers.loadConversation(ctx, payload.ConversationID)
	if err != nil {
		return err
	}
	if !found || conversation.Status != core.ConversationActive {
		return nil
	}
	messages, err := handlers.repository.ConversationMessages(ctx, conversation.ID)
	if err != nil {
		return err
	}
	prompt, found := latestUnansweredIncoming(messages)
	if !found || prompt.ID != payload.PromptMessageID || !replyablePrompt(prompt, handlers.clock.Now()) {
		return nil
	}
	if external := strings.TrimSpace(payload.PromptExternalID); external != "" && external != strings.TrimSpace(prompt.ExternalID) {
		return nil
	}
	decision := DecideChatReply(prompt.Text)
	if !decision.Reply {
		slog.Default().Info("conversation auto-reply skipped",
			"conversation", conversation.ID, "profile", conversation.ProfileID, "reason", decision.Reason)
		return nil
	}
	answer, err := generator(ctx, conversation, prompt, conversationReplyHistory(messages, prompt))
	if err != nil {
		return &core.OperationError{
			Category: core.ErrorTemporaryFailure, Operation: "conversations.reply",
			Platform: conversation.Platform, Message: "generate chat reply", Cause: err,
		}
	}
	text, ok := normalizeAutoReply(answer)
	if !ok {
		slog.Default().Info("conversation auto-reply produced no usable answer",
			"conversation", conversation.ID, "profile", conversation.ProfileID, "prompt", prompt.ID)
		return nil
	}
	if _, err := handlers.sendMessage(ctx, task, conversation, prompt.ID, text); err != nil {
		if conversationUnwritable(err) {
			return handlers.workflow.CloseConversationDueToPlatform(ctx, conversation.ID)
		}
		return err
	}
	return nil
}
