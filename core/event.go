package core

import (
	"encoding/json"
	"errors"
	"time"
)

type EventType string

const (
	EventVacancyDiscovered       EventType = "vacancy.discovered"
	EventVacancyInspected        EventType = "vacancy.inspected"
	EventApplicationSubmitted    EventType = "application.submitted"
	EventApplicationFailed       EventType = "application.failed"
	EventValidationRequired      EventType = "application.validation_required"
	EventConversationMessage     EventType = "conversation.message_received"
	EventConversationMarkedRead  EventType = "conversation.marked_read"
	EventQuestionnaireExtracted  EventType = "questionnaire.extracted"
	EventQuestionnaireCompleted  EventType = "questionnaire.completed"
	EventReviewRequested         EventType = "review.requested"
	EventReviewAnswered          EventType = "review.answered"
	EventResumePublished         EventType = "resume.published"
	EventChallengeCreated        EventType = "challenge.created"
	EventCalendarProposalCreated EventType = "calendar.proposal_created"
)

type Event struct {
	ID            EventID         `json:"id"`
	Type          EventType       `json:"type"`
	Source        string          `json:"source"`
	Platform      Platform        `json:"platform,omitempty"`
	ProfileID     ProfileID       `json:"profile_id,omitempty"`
	AggregateID   string          `json:"aggregate_id"`
	CorrelationID CorrelationID   `json:"correlation_id"`
	CausationID   string          `json:"causation_id,omitempty"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload"`
}

func (event Event) Validate() error {
	if event.ID == "" || event.Type == "" || event.Source == "" || event.AggregateID == "" || event.CorrelationID == "" {
		return errors.New("event requires id, type, source, aggregate id and correlation id")
	}
	if event.OccurredAt.IsZero() {
		return errors.New("event requires occurred_at")
	}
	if len(event.Payload) == 0 || !json.Valid(event.Payload) {
		return errors.New("event requires valid JSON payload")
	}
	return nil
}
