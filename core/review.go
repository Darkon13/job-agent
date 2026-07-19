package core

import (
	"errors"
	"fmt"
	"time"
)

type ReviewSessionStatus string

const (
	ReviewPending     ReviewSessionStatus = "pending"
	ReviewWaiting     ReviewSessionStatus = "waiting_answer"
	ReviewAnswered    ReviewSessionStatus = "answer_recorded"
	ReviewCompleted   ReviewSessionStatus = "completed"
	ReviewCancelled   ReviewSessionStatus = "cancelled"
	ReviewUnsupported ReviewSessionStatus = "unsupported"
	ReviewExpired     ReviewSessionStatus = "expired"
)

type AnswerAssessment string

const (
	AssessmentUnverified AnswerAssessment = "unverified"
	AssessmentAccepted   AnswerAssessment = "accepted"
	AssessmentRejected   AnswerAssessment = "rejected"
)

// ReviewSession represents one live test attempt. Starting it may consume a
// timed or limited platform attempt and therefore requires an explicit command.
type ReviewSession struct {
	ID               ReviewSessionID     `json:"id"`
	TestDefinitionID TestDefinitionID    `json:"test_definition_id"`
	Platform         Platform            `json:"platform"`
	ProfileID        ProfileID           `json:"profile_id"`
	CorrelationID    CorrelationID       `json:"correlation_id"`
	Status           ReviewSessionStatus `json:"status"`
	Revision         uint64              `json:"revision"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

// ReviewPrompt is delivered unchanged to REST/SSE, Telegram, or CLI clients.
// Runtime IDs stay here and never leak into reusable AnswerBlocks.
type ReviewPrompt struct {
	ID        ReviewPromptID  `json:"id"`
	SessionID ReviewSessionID `json:"session_id"`
	Revision  uint64          `json:"revision"`
	Question  Question        `json:"question"`
	Deadline  *time.Time      `json:"deadline,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// ReviewSelection is append-only history. Superseding a wrong selection adds a
// new revision instead of erasing the options or the earlier decision.
type ReviewSelection struct {
	PromptID        ReviewPromptID   `json:"prompt_id"`
	SessionID       ReviewSessionID  `json:"session_id"`
	Revision        uint64           `json:"revision"`
	SelectedOptions []string         `json:"selected_options,omitempty"`
	Text            string           `json:"text,omitempty"`
	Source          string           `json:"source"`
	Assessment      AnswerAssessment `json:"assessment"`
	SelectedAt      time.Time        `json:"selected_at"`
}

func NewReviewSession(id ReviewSessionID, definition TestDefinition, profileID ProfileID, correlationID CorrelationID, now time.Time) (ReviewSession, error) {
	if id == "" || definition.ID == "" || definition.Platform == "" || profileID == "" || correlationID == "" {
		return ReviewSession{}, errors.New("review session requires id, test definition, platform, profile and correlation id")
	}
	if now.IsZero() {
		return ReviewSession{}, errors.New("review session requires current time")
	}
	return ReviewSession{
		ID: id, TestDefinitionID: definition.ID, Platform: definition.Platform,
		ProfileID: profileID, CorrelationID: correlationID, Status: ReviewPending,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// RecordSelection validates optimistic concurrency and the shape of a client
// response. Persistence remains append-only and is handled by a repository.
func (session *ReviewSession) RecordSelection(prompt ReviewPrompt, selectedOptions []string, answerText, source string, expectedRevision uint64, now time.Time) (ReviewSelection, error) {
	if session == nil {
		return ReviewSelection{}, errors.New("review session is nil")
	}
	if session.Status == ReviewUnsupported {
		return ReviewSelection{}, errors.New("review session contains an unsupported question kind")
	}
	if prompt.SessionID != session.ID {
		return ReviewSelection{}, errors.New("review prompt belongs to another session")
	}
	if expectedRevision != session.Revision || prompt.Revision != session.Revision {
		return ReviewSelection{}, fmt.Errorf("stale review revision: got %d, current %d", expectedRevision, session.Revision)
	}
	if now.IsZero() || now.Before(session.UpdatedAt) {
		return ReviewSelection{}, errors.New("selection time must not move backwards")
	}
	if source == "" {
		return ReviewSelection{}, errors.New("review selection requires source")
	}
	if prompt.Deadline != nil && !now.Before(*prompt.Deadline) {
		session.Status = ReviewExpired
		session.UpdatedAt = now
		return ReviewSelection{}, errors.New("review prompt has expired")
	}
	if prompt.Question.Kind == QuestionCode {
		session.Status = ReviewUnsupported
		session.UpdatedAt = now
		return ReviewSelection{}, errors.New("code questions are not supported")
	}
	if err := validateSelection(prompt.Question, selectedOptions, answerText); err != nil {
		return ReviewSelection{}, err
	}

	selection := ReviewSelection{
		PromptID: prompt.ID, SessionID: session.ID, Revision: session.Revision,
		SelectedOptions: append([]string(nil), selectedOptions...), Text: answerText,
		Source: source, Assessment: AssessmentUnverified, SelectedAt: now,
	}
	session.Revision++
	session.Status = ReviewAnswered
	session.UpdatedAt = now
	return selection, nil
}

func validateSelection(question Question, selectedOptions []string, answerText string) error {
	switch question.Kind {
	case QuestionSingle:
		if answerText != "" || len(selectedOptions) != 1 {
			return errors.New("single-choice response requires exactly one selected option")
		}
	case QuestionMultiple:
		if answerText != "" || len(selectedOptions) == 0 {
			return errors.New("multiple-choice response requires selected options")
		}
	case QuestionText:
		if answerText == "" || len(selectedOptions) != 0 {
			return errors.New("text response requires text only")
		}
	case QuestionCode:
		return errors.New("code questions are not supported")
	default:
		return fmt.Errorf("unsupported question kind %q", question.Kind)
	}

	available := make(map[string]struct{}, len(question.Options))
	for _, option := range question.Options {
		available[NormalizeQuestionText(option.Text)] = struct{}{}
	}
	seen := make(map[string]struct{}, len(selectedOptions))
	for _, selected := range selectedOptions {
		key := NormalizeQuestionText(selected)
		if _, ok := available[key]; !ok {
			return fmt.Errorf("question %q has no option %q", question.Text, selected)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("option %q is selected more than once", selected)
		}
		seen[key] = struct{}{}
	}
	return nil
}
