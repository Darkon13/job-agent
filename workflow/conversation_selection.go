package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type SelectFollowUpRequest struct {
	ProfileID      core.ProfileID
	Strategy       core.FollowUpSelectionStrategy
	MinimumSilence time.Duration
	RunAfter       time.Duration
	DeadlineAfter  time.Duration
	Content        core.MessageContent
	Policy         core.FollowUpPolicy
	IdempotencyKey string
}

type SelectFollowUpResult struct {
	Conversation core.Conversation
	FollowUp     core.FollowUp
	Selected     bool
	Created      bool
}

// SelectAndScheduleFollowUp creates at most one durable reminder. An eligible
// conversation is active, its last meaningful message is outgoing, and the
// employer has not answered it. The task request key makes selection itself
// idempotent, including retries after the follow-up was already persisted.
func (workflow *ConversationWorkflow) SelectAndScheduleFollowUp(ctx context.Context, request SelectFollowUpRequest) (SelectFollowUpResult, error) {
	if workflow == nil {
		return SelectFollowUpResult{}, errors.New("conversation workflow is nil")
	}
	payload := core.ConversationFollowUpSelectPayload{
		ProfileID: request.ProfileID, Strategy: request.Strategy,
		MinimumSilence: core.Duration(request.MinimumSilence), RunAfter: core.Duration(request.RunAfter),
		DeadlineAfter: core.Duration(request.DeadlineAfter), Content: request.Content, Policy: request.Policy,
	}
	if err := payload.Validate(); err != nil {
		return SelectFollowUpResult{}, err
	}
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.IdempotencyKey == "" {
		return SelectFollowUpResult{}, errors.New("conversation follow-up selection requires idempotency key")
	}

	followUps, err := workflow.repository.ListFollowUps(ctx, storage.FollowUpFilter{ProfileID: request.ProfileID})
	if err != nil {
		return SelectFollowUpResult{}, fmt.Errorf("list profile follow-ups: %w", err)
	}
	for _, followUp := range followUps {
		key, keyErr := core.ConversationFollowUpRequestIdempotencyKey(followUp.ConversationID, request.IdempotencyKey)
		if keyErr != nil {
			return SelectFollowUpResult{}, keyErr
		}
		if followUp.IdempotencyKey != key {
			continue
		}
		conversation, loadErr := workflow.repository.Conversation(ctx, followUp.ConversationID)
		if loadErr != nil {
			return SelectFollowUpResult{}, fmt.Errorf("load idempotently selected conversation: %w", loadErr)
		}
		return SelectFollowUpResult{Conversation: conversation, FollowUp: followUp, Selected: true}, nil
	}

	conversations, err := workflow.repository.ListConversations(ctx, storage.ConversationFilter{
		ProfileID: request.ProfileID, Status: core.ConversationActive,
	})
	if err != nil {
		return SelectFollowUpResult{}, fmt.Errorf("list active conversations: %w", err)
	}
	now := workflow.clock.Now()
	candidates := eligibleFollowUpConversations(conversations, followUps, request, now)
	if len(candidates) == 0 {
		return SelectFollowUpResult{}, nil
	}
	selected := selectFollowUpConversation(candidates, request.Strategy, request.ProfileID, request.IdempotencyKey)
	runAt := now.Add(request.RunAfter)
	var deadline *time.Time
	if request.DeadlineAfter > 0 {
		value := now.Add(request.DeadlineAfter)
		deadline = &value
	}
	followUp, created, err := workflow.ScheduleFollowUp(ctx, ScheduleFollowUpRequest{
		ConversationID: selected.ID, AnchorMessageID: selected.LastMessageID, AnchorAt: *selected.LastOutgoingAt,
		RunAt: runAt, Deadline: deadline, Content: request.Content, Policy: request.Policy,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return SelectFollowUpResult{}, err
	}
	return SelectFollowUpResult{Conversation: selected, FollowUp: followUp, Selected: true, Created: created}, nil
}

func eligibleFollowUpConversations(conversations []core.Conversation, followUps []core.FollowUp, request SelectFollowUpRequest, now time.Time) []core.Conversation {
	type followUpState struct {
		pending int
		sent    int
	}
	states := make(map[core.ConversationID]followUpState)
	for _, followUp := range followUps {
		state := states[followUp.ConversationID]
		switch followUp.Status {
		case core.FollowUpScheduled, core.FollowUpQueued:
			state.pending++
		case core.FollowUpSent:
			state.sent++
		}
		states[followUp.ConversationID] = state
	}
	result := make([]core.Conversation, 0, len(conversations))
	for _, conversation := range conversations {
		if conversation.Status != core.ConversationActive || conversation.LastOutgoingAt == nil || conversation.LastMessageAt == nil ||
			conversation.LastMessageID == "" || !conversation.LastMessageAt.Equal(*conversation.LastOutgoingAt) {
			continue
		}
		if conversation.LastIncomingAt != nil && !conversation.LastOutgoingAt.After(*conversation.LastIncomingAt) {
			continue
		}
		if now.Before(conversation.LastOutgoingAt.Add(request.MinimumSilence)) {
			continue
		}
		state := states[conversation.ID]
		if state.pending > 0 || state.sent >= request.Policy.MaxFollowUps {
			continue
		}
		if request.Policy.Cooldown > 0 && now.Before(conversation.LastOutgoingAt.Add(request.Policy.Cooldown.Value())) {
			continue
		}
		result = append(result, conversation)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].LastOutgoingAt.Equal(*result[j].LastOutgoingAt) {
			return result[i].LastOutgoingAt.Before(*result[j].LastOutgoingAt)
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func selectFollowUpConversation(candidates []core.Conversation, strategy core.FollowUpSelectionStrategy, profileID core.ProfileID, requestKey string) core.Conversation {
	switch strategy {
	case core.FollowUpSelectNewestUnanswered:
		return candidates[len(candidates)-1]
	case core.FollowUpSelectRandom:
		digest := sha256.Sum256([]byte(string(profileID) + "\x00" + requestKey))
		index := binary.BigEndian.Uint64(digest[:8]) % uint64(len(candidates))
		return candidates[index]
	default:
		return candidates[0]
	}
}
