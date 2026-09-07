package core

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type ConversationStatus string

const (
	ConversationActive   ConversationStatus = "active"
	ConversationClosed   ConversationStatus = "closed"
	ConversationRejected ConversationStatus = "rejected"
	ConversationArchived ConversationStatus = "archived"
)

type MessageDirection string

const (
	MessageIncoming MessageDirection = "incoming"
	MessageOutgoing MessageDirection = "outgoing"
)

type MessageKind string

const (
	MessageText          MessageKind = "text"
	MessageSuggestion    MessageKind = "suggestion"
	MessageQuestionnaire MessageKind = "questionnaire"
	MessageSystem        MessageKind = "system"
)

type MessageStatus string

const (
	MessageObserved  MessageStatus = "observed"
	MessageQueued    MessageStatus = "queued"
	MessageSent      MessageStatus = "sent"
	MessageFailed    MessageStatus = "failed"
	MessageCancelled MessageStatus = "cancelled"
)

type MessageOption struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// ConversationMessage is a normalized message observed on or sent to a platform.
// Adapter-specific payloads stay behind the adapter boundary.
type ConversationMessage struct {
	ID             MessageID        `json:"id"`
	ConversationID ConversationID   `json:"conversation_id"`
	ExternalID     string           `json:"external_id,omitempty"`
	ReplyToID      MessageID        `json:"reply_to_id,omitempty"`
	Direction      MessageDirection `json:"direction"`
	Kind           MessageKind      `json:"kind"`
	Status         MessageStatus    `json:"status"`
	Text           string           `json:"text,omitempty"`
	Options        []MessageOption  `json:"options,omitempty"`
	OccurredAt     time.Time        `json:"occurred_at"`
}

func (message ConversationMessage) Validate() error {
	if message.ID == "" || message.ConversationID == "" {
		return errors.New("conversation message requires id and conversation id")
	}
	if message.OccurredAt.IsZero() {
		return errors.New("conversation message requires occurred_at")
	}
	switch message.Direction {
	case MessageIncoming:
		if message.Status != MessageObserved {
			return errors.New("incoming conversation message must be observed")
		}
	case MessageOutgoing:
		switch message.Status {
		case MessageQueued, MessageSent, MessageFailed, MessageCancelled:
		default:
			return errors.New("outgoing conversation message has invalid status")
		}
	default:
		return errors.New("conversation message has invalid direction")
	}
	switch message.Kind {
	case MessageText, MessageSuggestion, MessageQuestionnaire, MessageSystem:
	default:
		return errors.New("conversation message has invalid kind")
	}
	if strings.TrimSpace(message.Text) == "" && len(message.Options) == 0 {
		return errors.New("conversation message requires text or options")
	}
	for index, option := range message.Options {
		if strings.TrimSpace(option.ID) == "" || strings.TrimSpace(option.Text) == "" {
			return fmt.Errorf("conversation message option %d requires id and text", index)
		}
	}
	return nil
}

type Conversation struct {
	ID             ConversationID     `json:"id"`
	Platform       Platform           `json:"platform"`
	ProfileID      ProfileID          `json:"profile_id"`
	ExternalID     string             `json:"external_id"`
	ApplicationID  ApplicationID      `json:"application_id,omitempty"`
	Status         ConversationStatus `json:"status"`
	LastMessageID  MessageID          `json:"last_message_id,omitempty"`
	LastMessageAt  *time.Time         `json:"last_message_at,omitempty"`
	LastIncomingAt *time.Time         `json:"last_incoming_at,omitempty"`
	LastOutgoingAt *time.Time         `json:"last_outgoing_at,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
	Revision       uint64             `json:"revision"`
}

func (conversation Conversation) Validate() error {
	if conversation.ID == "" || conversation.Platform == "" || conversation.ProfileID == "" || strings.TrimSpace(conversation.ExternalID) == "" {
		return errors.New("conversation requires id, platform, profile and external id")
	}
	switch conversation.Status {
	case ConversationActive, ConversationClosed, ConversationRejected, ConversationArchived:
	default:
		return errors.New("invalid conversation status")
	}
	if conversation.CreatedAt.IsZero() || conversation.UpdatedAt.Before(conversation.CreatedAt) || conversation.Revision < 1 {
		return errors.New("conversation requires valid timestamps and revision")
	}
	return nil
}

func NewConversation(id ConversationID, platform Platform, profileID ProfileID, externalID string, now time.Time) (Conversation, error) {
	if id == "" || platform == "" || profileID == "" || strings.TrimSpace(externalID) == "" {
		return Conversation{}, errors.New("conversation requires id, platform, profile and external id")
	}
	if now.IsZero() {
		return Conversation{}, errors.New("conversation requires current time")
	}
	return Conversation{
		ID: id, Platform: platform, ProfileID: profileID, ExternalID: externalID,
		Status: ConversationActive, CreatedAt: now, UpdatedAt: now, Revision: 1,
	}, nil
}

func (conversation *Conversation) Observe(message ConversationMessage, now time.Time) (bool, error) {
	if conversation == nil {
		return false, errors.New("conversation is nil")
	}
	if err := message.Validate(); err != nil {
		return false, err
	}
	if message.ConversationID != conversation.ID {
		return false, errors.New("message belongs to another conversation")
	}
	if now.IsZero() || now.Before(conversation.UpdatedAt) {
		return false, errors.New("conversation observation time must not move backwards")
	}
	if conversation.LastMessageID == message.ID {
		return false, nil
	}
	observedAt := message.OccurredAt
	if conversation.LastMessageAt == nil || !observedAt.Before(*conversation.LastMessageAt) {
		conversation.LastMessageID = message.ID
		conversation.LastMessageAt = &observedAt
	}
	if message.Direction == MessageIncoming {
		if conversation.LastIncomingAt == nil || observedAt.After(*conversation.LastIncomingAt) {
			conversation.LastIncomingAt = &observedAt
		}
	} else if message.Status == MessageSent {
		if conversation.LastOutgoingAt == nil || observedAt.After(*conversation.LastOutgoingAt) {
			conversation.LastOutgoingAt = &observedAt
		}
	}
	conversation.UpdatedAt = now
	conversation.Revision++
	return true, nil
}

func (conversation *Conversation) SetStatus(status ConversationStatus, now time.Time) error {
	if conversation == nil {
		return errors.New("conversation is nil")
	}
	switch status {
	case ConversationActive, ConversationClosed, ConversationRejected, ConversationArchived:
	default:
		return errors.New("invalid conversation status")
	}
	if now.IsZero() || now.Before(conversation.UpdatedAt) {
		return errors.New("conversation status time must not move backwards")
	}
	if conversation.Status == status {
		return nil
	}
	conversation.Status = status
	conversation.UpdatedAt = now
	conversation.Revision++
	return nil
}

// MessageContent references exactly one source for an outgoing message.
// OperatorTag may point to a chain with deterministic/template fallbacks.
type MessageContent struct {
	Text        string `json:"text,omitempty"`
	TemplateTag string `json:"template,omitempty"`
	OperatorTag string `json:"operator,omitempty"`
}

func (content MessageContent) Validate() error {
	sources := 0
	for _, value := range []string{content.Text, content.TemplateTag, content.OperatorTag} {
		if strings.TrimSpace(value) != "" {
			sources++
		}
	}
	if sources != 1 {
		return errors.New("message content requires exactly one of text, template or operator")
	}
	return nil
}

type FollowUpStatus string

const (
	FollowUpScheduled FollowUpStatus = "scheduled"
	FollowUpQueued    FollowUpStatus = "queued"
	FollowUpSent      FollowUpStatus = "sent"
	FollowUpCancelled FollowUpStatus = "cancelled"
	FollowUpFailed    FollowUpStatus = "failed"
	FollowUpExpired   FollowUpStatus = "expired"
)

// FollowUpSelectionStrategy chooses one active conversation which is waiting
// for an employer response. Selection never turns an incoming message awaiting
// the applicant into an automatic reminder.
type FollowUpSelectionStrategy string

const (
	FollowUpSelectOldestUnanswered FollowUpSelectionStrategy = "oldest_unanswered"
	FollowUpSelectNewestUnanswered FollowUpSelectionStrategy = "newest_unanswered"
	FollowUpSelectRandom           FollowUpSelectionStrategy = "random"
)

func (strategy FollowUpSelectionStrategy) Validate() error {
	switch strategy {
	case FollowUpSelectOldestUnanswered, FollowUpSelectNewestUnanswered, FollowUpSelectRandom:
		return nil
	default:
		return fmt.Errorf("invalid follow-up selection strategy %q", strategy)
	}
}

type FollowUpCancelReason string

const (
	FollowUpIncomingReceived     FollowUpCancelReason = "incoming_received"
	FollowUpConversationInactive FollowUpCancelReason = "conversation_inactive"
	FollowUpApplicationTerminal  FollowUpCancelReason = "application_terminal"
	FollowUpSuperseded           FollowUpCancelReason = "superseded"
	FollowUpUserRequested        FollowUpCancelReason = "user_requested"
	FollowUpPolicyDenied         FollowUpCancelReason = "policy_denied"
)

type FollowUpPolicy struct {
	CancelOnIncoming          bool     `json:"cancel_on_incoming"`
	RequireActiveConversation bool     `json:"require_active_conversation"`
	MaxFollowUps              int      `json:"max_follow_ups"`
	Cooldown                  Duration `json:"cooldown"`
}

func (policy FollowUpPolicy) Validate() error {
	if policy.MaxFollowUps < 1 {
		return errors.New("follow-up policy requires a positive max_follow_ups")
	}
	if policy.Cooldown < 0 {
		return errors.New("follow-up policy cooldown must not be negative")
	}
	return nil
}

type FollowUp struct {
	ID              FollowUpID           `json:"id"`
	ConversationID  ConversationID       `json:"conversation_id"`
	ProfileID       ProfileID            `json:"profile_id"`
	Platform        Platform             `json:"platform"`
	AnchorMessageID MessageID            `json:"anchor_message_id,omitempty"`
	AnchorAt        time.Time            `json:"anchor_at"`
	RunAt           time.Time            `json:"run_at"`
	Deadline        *time.Time           `json:"deadline,omitempty"`
	Content         MessageContent       `json:"content"`
	Policy          FollowUpPolicy       `json:"policy"`
	Status          FollowUpStatus       `json:"status"`
	IdempotencyKey  string               `json:"idempotency_key"`
	SentMessageID   MessageID            `json:"sent_message_id,omitempty"`
	CancelReason    FollowUpCancelReason `json:"cancel_reason,omitempty"`
	FailureMessage  string               `json:"failure_message,omitempty"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
	Revision        uint64               `json:"revision"`
}

func (followUp FollowUp) Validate() error {
	if followUp.ID == "" || followUp.ConversationID == "" || followUp.ProfileID == "" || followUp.Platform == "" || strings.TrimSpace(followUp.IdempotencyKey) == "" {
		return errors.New("follow-up requires id, conversation, profile, platform and idempotency key")
	}
	if followUp.AnchorAt.IsZero() || followUp.CreatedAt.IsZero() || followUp.RunAt.Before(followUp.AnchorAt) ||
		followUp.RunAt.Before(followUp.CreatedAt) || followUp.UpdatedAt.Before(followUp.CreatedAt) || followUp.Revision < 1 {
		return errors.New("follow-up requires valid timestamps and revision")
	}
	if followUp.Deadline != nil && !followUp.Deadline.After(followUp.RunAt) {
		return errors.New("follow-up deadline must be after run time")
	}
	if err := followUp.Content.Validate(); err != nil {
		return err
	}
	if err := followUp.Policy.Validate(); err != nil {
		return err
	}
	switch followUp.Status {
	case FollowUpScheduled, FollowUpQueued, FollowUpSent, FollowUpCancelled, FollowUpFailed, FollowUpExpired:
	default:
		return errors.New("invalid follow-up status")
	}
	if followUp.Status == FollowUpSent && followUp.SentMessageID == "" {
		return errors.New("sent follow-up requires sent message id")
	}
	if followUp.Status == FollowUpCancelled && followUp.CancelReason == "" {
		return errors.New("cancelled follow-up requires cancellation reason")
	}
	if followUp.Status == FollowUpFailed && strings.TrimSpace(followUp.FailureMessage) == "" {
		return errors.New("failed follow-up requires failure message")
	}
	return nil
}

type NewFollowUpParams struct {
	ID              FollowUpID
	ConversationID  ConversationID
	ProfileID       ProfileID
	Platform        Platform
	AnchorMessageID MessageID
	AnchorAt        time.Time
	RunAt           time.Time
	Deadline        *time.Time
	Content         MessageContent
	Policy          FollowUpPolicy
	IdempotencyKey  string
}

func NewFollowUp(params NewFollowUpParams, now time.Time) (FollowUp, error) {
	if params.ID == "" || params.ConversationID == "" || params.ProfileID == "" || params.Platform == "" || strings.TrimSpace(params.IdempotencyKey) == "" {
		return FollowUp{}, errors.New("follow-up requires id, conversation, profile, platform and idempotency key")
	}
	if now.IsZero() || params.AnchorAt.IsZero() || params.RunAt.IsZero() {
		return FollowUp{}, errors.New("follow-up requires current, anchor and run times")
	}
	if params.RunAt.Before(now) || params.RunAt.Before(params.AnchorAt) {
		return FollowUp{}, errors.New("follow-up run time must not precede creation or anchor")
	}
	if params.Deadline != nil && !params.Deadline.After(params.RunAt) {
		return FollowUp{}, errors.New("follow-up deadline must be after run time")
	}
	if err := params.Content.Validate(); err != nil {
		return FollowUp{}, err
	}
	if err := params.Policy.Validate(); err != nil {
		return FollowUp{}, err
	}
	var deadline *time.Time
	if params.Deadline != nil {
		value := *params.Deadline
		deadline = &value
	}
	followUp := FollowUp{
		ID: params.ID, ConversationID: params.ConversationID, ProfileID: params.ProfileID,
		Platform: params.Platform, AnchorMessageID: params.AnchorMessageID, AnchorAt: params.AnchorAt,
		RunAt: params.RunAt, Deadline: deadline, Content: params.Content, Policy: params.Policy,
		Status: FollowUpScheduled, IdempotencyKey: params.IdempotencyKey,
		CreatedAt: now, UpdatedAt: now, Revision: 1,
	}
	return followUp, followUp.Validate()
}

// CancellationFor performs the guard that must run again immediately before send.
func (followUp FollowUp) CancellationFor(conversation Conversation) (FollowUpCancelReason, bool, error) {
	if conversation.ID != followUp.ConversationID || conversation.ProfileID != followUp.ProfileID || conversation.Platform != followUp.Platform {
		return "", false, errors.New("follow-up and conversation identity mismatch")
	}
	if followUp.Policy.RequireActiveConversation && conversation.Status != ConversationActive {
		return FollowUpConversationInactive, true, nil
	}
	if followUp.Policy.CancelOnIncoming && conversation.LastIncomingAt != nil && conversation.LastIncomingAt.After(followUp.AnchorAt) {
		return FollowUpIncomingReceived, true, nil
	}
	return "", false, nil
}

func (followUp *FollowUp) Queue(now time.Time) error {
	if followUp == nil {
		return errors.New("follow-up is nil")
	}
	if now.Before(followUp.RunAt) {
		return errors.New("follow-up is not due")
	}
	if followUp.Deadline != nil && !now.Before(*followUp.Deadline) {
		return errors.New("follow-up deadline has expired")
	}
	return followUp.transition(FollowUpQueued, now)
}

func (followUp *FollowUp) MarkSent(messageID MessageID, now time.Time) error {
	if messageID == "" {
		return errors.New("sent follow-up requires message id")
	}
	if err := followUp.transition(FollowUpSent, now); err != nil {
		return err
	}
	followUp.SentMessageID = messageID
	return nil
}

func (followUp *FollowUp) Cancel(reason FollowUpCancelReason, now time.Time) error {
	switch reason {
	case FollowUpIncomingReceived, FollowUpConversationInactive, FollowUpApplicationTerminal,
		FollowUpSuperseded, FollowUpUserRequested, FollowUpPolicyDenied:
	default:
		return errors.New("follow-up cancellation requires a valid reason")
	}
	if err := followUp.transition(FollowUpCancelled, now); err != nil {
		return err
	}
	followUp.CancelReason = reason
	return nil
}

func (followUp *FollowUp) MarkFailed(message string, now time.Time) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("failed follow-up requires failure message")
	}
	if err := followUp.transition(FollowUpFailed, now); err != nil {
		return err
	}
	followUp.FailureMessage = message
	return nil
}

func (followUp *FollowUp) Expire(now time.Time) error {
	return followUp.transition(FollowUpExpired, now)
}

func (followUp *FollowUp) Reschedule(runAt time.Time, deadline *time.Time, expectedRevision uint64, now time.Time) error {
	if followUp == nil {
		return errors.New("follow-up is nil")
	}
	if followUp.Status != FollowUpScheduled {
		return errors.New("only a scheduled follow-up can be rescheduled")
	}
	if followUp.Revision != expectedRevision {
		return errors.New("follow-up revision conflict")
	}
	if now.IsZero() || now.Before(followUp.UpdatedAt) || runAt.Before(now) {
		return errors.New("invalid follow-up reschedule time")
	}
	if deadline != nil && !deadline.After(runAt) {
		return errors.New("follow-up deadline must be after run time")
	}
	followUp.RunAt = runAt
	if deadline == nil {
		followUp.Deadline = nil
	} else {
		value := *deadline
		followUp.Deadline = &value
	}
	followUp.UpdatedAt = now
	followUp.Revision++
	return nil
}

func (followUp *FollowUp) transition(to FollowUpStatus, now time.Time) error {
	if followUp == nil {
		return errors.New("follow-up is nil")
	}
	if now.IsZero() || now.Before(followUp.UpdatedAt) {
		return errors.New("follow-up transition time must not move backwards")
	}
	allowed := map[FollowUpStatus]map[FollowUpStatus]struct{}{
		FollowUpScheduled: {FollowUpQueued: {}, FollowUpCancelled: {}, FollowUpExpired: {}},
		FollowUpQueued:    {FollowUpSent: {}, FollowUpCancelled: {}, FollowUpFailed: {}, FollowUpExpired: {}},
	}
	if _, ok := allowed[followUp.Status][to]; !ok {
		return fmt.Errorf("follow-up transition %s -> %s is not allowed", followUp.Status, to)
	}
	followUp.Status = to
	followUp.UpdatedAt = now
	followUp.Revision++
	return nil
}
