package memory

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func (repository *Repository) CreateConversation(ctx context.Context, candidate core.Conversation) (core.Conversation, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.Conversation{}, false, err
	}
	if err := candidate.Validate(); err != nil {
		return core.Conversation{}, false, err
	}
	if candidate.Status != core.ConversationActive || candidate.Revision != 1 {
		return core.Conversation{}, false, errors.New("conversation repository accepts only initialized active conversations")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if stored, exists := repository.conversations[candidate.ID]; exists {
		if !sameConversationIdentity(stored, candidate) {
			return core.Conversation{}, false, errors.New("conversation id conflicts with a different conversation")
		}
		return cloneConversation(stored), false, nil
	}
	externalKey := conversationExternalKey{platform: candidate.Platform, profileID: candidate.ProfileID, externalID: candidate.ExternalID}
	if existingID, exists := repository.conversationExternal[externalKey]; exists {
		stored := repository.conversations[existingID]
		if stored.ApplicationID != candidate.ApplicationID {
			return core.Conversation{}, false, errors.New("conversation external id conflicts with a different application")
		}
		return cloneConversation(stored), false, nil
	}
	repository.conversations[candidate.ID] = cloneConversation(candidate)
	repository.conversationExternal[externalKey] = candidate.ID
	return cloneConversation(candidate), true, nil
}

func (repository *Repository) Conversation(ctx context.Context, id core.ConversationID) (core.Conversation, error) {
	if err := ctx.Err(); err != nil {
		return core.Conversation{}, err
	}
	if id == "" {
		return core.Conversation{}, errors.New("conversation id is required")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	conversation, exists := repository.conversations[id]
	if !exists {
		return core.Conversation{}, errors.New("conversation not found")
	}
	return cloneConversation(conversation), nil
}

func (repository *Repository) SaveConversation(ctx context.Context, candidate core.Conversation, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Revision != expectedRevision+1 {
		return errors.New("conversation candidate must advance revision exactly once")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.conversations[candidate.ID]
	if !exists || stored.Revision != expectedRevision {
		return storage.ErrRevisionConflict
	}
	if !sameConversationIdentity(stored, candidate) || !sameConversationTimeline(stored, candidate) {
		return errors.New("conversation immutable identity or timeline changed")
	}
	repository.conversations[candidate.ID] = cloneConversation(candidate)
	return nil
}

func (repository *Repository) ListConversations(ctx context.Context, filter storage.ConversationFilter) ([]core.Conversation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.Conversation, 0, len(repository.conversations))
	for _, conversation := range repository.conversations {
		if !conversationMatchesFilter(conversation, filter) {
			continue
		}
		result = append(result, cloneConversation(conversation))
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].UpdatedAt.After(result[j].UpdatedAt)
		}
		return result[i].ID < result[j].ID
	})
	if filter.Offset > 0 {
		if filter.Offset >= len(result) {
			return []core.Conversation{}, nil
		}
		result = result[filter.Offset:]
	}
	if filter.Limit > 0 && filter.Limit < len(result) {
		result = result[:filter.Limit]
	}
	return result, nil
}

// conversationMatchesFilter keeps the in-memory behaviour aligned with SQL.
func conversationMatchesFilter(conversation core.Conversation, filter storage.ConversationFilter) bool {
	if filter.Platform != "" && conversation.Platform != filter.Platform ||
		filter.ProfileID != "" && conversation.ProfileID != filter.ProfileID ||
		filter.Status != "" && conversation.Status != filter.Status {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	if query == "" {
		return true
	}
	for _, value := range []string{conversation.VacancyTitle, conversation.Employer, string(conversation.ExternalID)} {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

// CountConversations summarizes the same filtered set as ListConversations.
func (repository *Repository) CountConversations(ctx context.Context, filter storage.ConversationFilter) (storage.ConversationCounts, error) {
	if err := ctx.Err(); err != nil {
		return storage.ConversationCounts{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	var counts storage.ConversationCounts
	for _, conversation := range repository.conversations {
		if !conversationMatchesFilter(conversation, filter) {
			continue
		}
		counts.Total++
		counts.Unread += conversation.UnreadCount
		if conversation.Status == core.ConversationActive {
			counts.Active++
		}
	}
	return counts, nil
}

func (repository *Repository) AppendConversationMessage(ctx context.Context, message core.ConversationMessage, observedAt time.Time) (core.Conversation, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.Conversation{}, false, err
	}
	if err := message.Validate(); err != nil {
		return core.Conversation{}, false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	conversation, exists := repository.conversations[message.ConversationID]
	if !exists {
		return core.Conversation{}, false, errors.New("conversation not found")
	}
	conversationMessages := repository.messages[message.ConversationID]
	if conversationMessages == nil {
		conversationMessages = make(map[core.MessageID]core.ConversationMessage)
		repository.messages[message.ConversationID] = conversationMessages
	}
	if stored, exists := conversationMessages[message.ID]; exists {
		if !sameMessage(stored, message) {
			return core.Conversation{}, false, errors.New("message id conflicts with a different message")
		}
		return cloneConversation(conversation), false, nil
	}
	if message.ExternalID != "" {
		for _, stored := range conversationMessages {
			if stored.ExternalID != message.ExternalID {
				continue
			}
			if !sameMessagePayload(stored, message) {
				return core.Conversation{}, false, storage.ErrConversationMessageConflict
			}
			return cloneConversation(conversation), false, nil
		}
	}
	if _, err := conversation.Observe(message, observedAt); err != nil {
		return core.Conversation{}, false, err
	}
	conversationMessages[message.ID] = cloneMessage(message)
	repository.conversations[conversation.ID] = conversation
	return cloneConversation(conversation), true, nil
}

// OpenQuestionnaireConversationIDs lists conversations whose latest incoming
// questionnaire still has no outgoing answer after it.
func (repository *Repository) OpenQuestionnaireConversationIDs(ctx context.Context) ([]core.ConversationID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.ConversationID, 0)
	for id, messages := range repository.messages {
		var lastOutgoing, latestIncoming time.Time
		for _, message := range messages {
			if message.Direction == core.MessageOutgoing && (message.Status == core.MessageSent || message.Status == core.MessageQueued) &&
				message.OccurredAt.After(lastOutgoing) {
				lastOutgoing = message.OccurredAt
			}
		}
		for _, message := range messages {
			if message.Direction == core.MessageIncoming && message.Kind == core.MessageQuestionnaire && len(message.Options) > 0 &&
				message.OccurredAt.After(lastOutgoing) && message.OccurredAt.After(latestIncoming) {
				latestIncoming = message.OccurredAt
			}
		}
		if !latestIncoming.IsZero() {
			result = append(result, id)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func (repository *Repository) ConversationsAwaitingQuestionnaire(ctx context.Context, profileID core.ProfileID, since time.Time, limit int) ([]core.ConversationID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if profileID == "" {
		return nil, errors.New("awaiting questionnaire requires a profile")
	}
	if limit <= 0 {
		return nil, nil
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	type pending struct {
		id    core.ConversationID
		after time.Time
	}
	items := make([]pending, 0)
	for id, messages := range repository.messages {
		conversation, exists := repository.conversations[id]
		if !exists || conversation.ProfileID != profileID {
			continue
		}
		lastOutgoing := time.Time{}
		for _, message := range messages {
			if message.Direction == core.MessageOutgoing && (message.Status == core.MessageSent || message.Status == core.MessageQueued) &&
				message.OccurredAt.After(lastOutgoing) {
				lastOutgoing = message.OccurredAt
			}
		}
		latestIncoming := time.Time{}
		for _, message := range messages {
			if message.Direction == core.MessageIncoming && message.Kind == core.MessageQuestionnaire && len(message.Options) > 0 &&
				message.OccurredAt.After(lastOutgoing) && message.OccurredAt.After(latestIncoming) {
				latestIncoming = message.OccurredAt
			}
		}
		if !latestIncoming.IsZero() && !latestIncoming.Before(since) {
			items = append(items, pending{id: id, after: latestIncoming})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].after.Equal(items[j].after) {
			return items[i].after.After(items[j].after)
		}
		return items[i].id < items[j].id
	})
	if len(items) > limit {
		items = items[:limit]
	}
	result := make([]core.ConversationID, 0, len(items))
	for _, item := range items {
		result = append(result, item.id)
	}
	return result, nil
}

func (repository *Repository) ConversationMessages(ctx context.Context, id core.ConversationID) ([]core.ConversationMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if _, exists := repository.conversations[id]; !exists {
		return nil, errors.New("conversation not found")
	}
	stored := repository.messages[id]
	result := make([]core.ConversationMessage, 0, len(stored))
	for _, message := range stored {
		result = append(result, cloneMessage(message))
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].OccurredAt.Before(result[j].OccurredAt)
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func (repository *Repository) CreateFollowUp(ctx context.Context, candidate core.FollowUp) (core.FollowUp, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.FollowUp{}, false, err
	}
	if err := candidate.Validate(); err != nil {
		return core.FollowUp{}, false, err
	}
	if candidate.Status != core.FollowUpScheduled || candidate.Revision != 1 {
		return core.FollowUp{}, false, errors.New("follow-up repository accepts only initialized scheduled follow-ups")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	conversation, exists := repository.conversations[candidate.ConversationID]
	if !exists || conversation.ProfileID != candidate.ProfileID || conversation.Platform != candidate.Platform {
		return core.FollowUp{}, false, errors.New("follow-up references an unknown or mismatched conversation")
	}
	if stored, exists := repository.followUps[candidate.ID]; exists {
		if stored.IdempotencyKey != candidate.IdempotencyKey || !sameFollowUpRequest(stored, candidate) {
			return core.FollowUp{}, false, errors.New("follow-up id conflicts with a different timer")
		}
		return cloneFollowUp(stored), false, nil
	}
	if existingID, exists := repository.followUpKeys[candidate.IdempotencyKey]; exists {
		stored := repository.followUps[existingID]
		if !sameFollowUpRequest(stored, candidate) {
			return core.FollowUp{}, false, errors.New("follow-up idempotency key conflicts with a different timer")
		}
		return cloneFollowUp(stored), false, nil
	}
	repository.followUps[candidate.ID] = cloneFollowUp(candidate)
	repository.followUpKeys[candidate.IdempotencyKey] = candidate.ID
	return cloneFollowUp(candidate), true, nil
}

func (repository *Repository) FollowUp(ctx context.Context, id core.FollowUpID) (core.FollowUp, error) {
	if err := ctx.Err(); err != nil {
		return core.FollowUp{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	followUp, exists := repository.followUps[id]
	if !exists {
		return core.FollowUp{}, errors.New("follow-up not found")
	}
	return cloneFollowUp(followUp), nil
}

func (repository *Repository) SaveFollowUp(ctx context.Context, candidate core.FollowUp, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Revision != expectedRevision+1 {
		return errors.New("follow-up candidate must advance revision exactly once")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.followUps[candidate.ID]
	if !exists || stored.Revision != expectedRevision {
		return storage.ErrRevisionConflict
	}
	if !sameFollowUpIdentity(stored, candidate) {
		return errors.New("follow-up immutable identity changed")
	}
	repository.followUps[candidate.ID] = cloneFollowUp(candidate)
	return nil
}

func (repository *Repository) ListFollowUps(ctx context.Context, filter storage.FollowUpFilter) ([]core.FollowUp, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.FollowUp, 0, len(repository.followUps))
	for _, followUp := range repository.followUps {
		if filter.ConversationID != "" && followUp.ConversationID != filter.ConversationID ||
			filter.ProfileID != "" && followUp.ProfileID != filter.ProfileID ||
			filter.Status != "" && followUp.Status != filter.Status ||
			filter.DueBefore != nil && followUp.RunAt.After(*filter.DueBefore) {
			continue
		}
		result = append(result, cloneFollowUp(followUp))
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].RunAt.Equal(result[j].RunAt) {
			return result[i].RunAt.Before(result[j].RunAt)
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func sameConversationIdentity(first, second core.Conversation) bool {
	return first.ID == second.ID && first.Platform == second.Platform && first.ProfileID == second.ProfileID &&
		first.ExternalID == second.ExternalID && first.ApplicationID == second.ApplicationID
}

func sameConversationTimeline(first, second core.Conversation) bool {
	return first.CreatedAt.Equal(second.CreatedAt) && first.LastMessageID == second.LastMessageID &&
		equalTime(first.LastMessageAt, second.LastMessageAt) && equalTime(first.LastIncomingAt, second.LastIncomingAt) &&
		equalTime(first.LastOutgoingAt, second.LastOutgoingAt)
}

func sameMessage(first, second core.ConversationMessage) bool {
	return first.ID == second.ID && sameMessagePayload(first, second)
}

func sameMessagePayload(first, second core.ConversationMessage) bool {
	if first.ConversationID != second.ConversationID || first.ExternalID != second.ExternalID ||
		!sameMessageReply(first.ReplyToID, second.ReplyToID) || first.Direction != second.Direction || first.Kind != second.Kind ||
		first.Status != second.Status || first.Text != second.Text || !sameMessageTime(first.OccurredAt, second.OccurredAt) || len(first.Options) != len(second.Options) {
		return false
	}
	for index := range first.Options {
		if first.Options[index] != second.Options[index] {
			return false
		}
	}
	return true
}

// sameMessageReply tolerates a missing reply link on one side: the local send
// stores the prompt it answered, while synced platform history often omits it.
func sameMessageReply(first, second core.MessageID) bool {
	return first == second || first == "" || second == ""
}

// sameMessageTime compares platform timestamps at millisecond precision: HH
// returns milliseconds for synced history but can carry sub-millisecond
// fractions on a send response for the very same message.
func sameMessageTime(first, second time.Time) bool {
	return first.Truncate(time.Millisecond).Equal(second.Truncate(time.Millisecond))
}

func sameFollowUpRequest(first, second core.FollowUp) bool {
	return first.ConversationID == second.ConversationID && first.ProfileID == second.ProfileID &&
		first.Platform == second.Platform && first.AnchorMessageID == second.AnchorMessageID &&
		first.AnchorAt.Equal(second.AnchorAt) && first.RunAt.Equal(second.RunAt) && equalTime(first.Deadline, second.Deadline) &&
		first.Content == second.Content && first.Policy == second.Policy
}

func sameFollowUpIdentity(first, second core.FollowUp) bool {
	return first.ID == second.ID && first.ConversationID == second.ConversationID && first.ProfileID == second.ProfileID &&
		first.Platform == second.Platform && first.IdempotencyKey == second.IdempotencyKey && first.CreatedAt.Equal(second.CreatedAt)
}

func cloneConversation(source core.Conversation) core.Conversation {
	result := source
	result.LastMessageAt = cloneTime(source.LastMessageAt)
	result.LastIncomingAt = cloneTime(source.LastIncomingAt)
	result.LastOutgoingAt = cloneTime(source.LastOutgoingAt)
	return result
}

func cloneMessage(source core.ConversationMessage) core.ConversationMessage {
	result := source
	result.Options = append([]core.MessageOption(nil), source.Options...)
	return result
}

func cloneFollowUp(source core.FollowUp) core.FollowUp {
	result := source
	result.Deadline = cloneTime(source.Deadline)
	return result
}

func cloneTime(source *time.Time) *time.Time {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func equalTime(first, second *time.Time) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.Equal(*second)
}
