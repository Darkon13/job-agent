package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ReviewAnswerHandler records one human selection for the current review
// prompt and, when the reviewed answers now cover the whole questionnaire,
// resumes the automatic submit chain. A stale revision never overwrites a
// newer answer.
type ReviewAnswerHandler struct {
	reviews  storage.ReviewRepository
	clock    Clock
	resolver VacancyAnswerBlockResolver
	chain    VacancyTestEnqueuer
}

func NewReviewAnswerHandler(reviews storage.ReviewRepository, clock Clock) (*ReviewAnswerHandler, error) {
	if reviews == nil || clock == nil {
		return nil, errors.New("review answer handler requires review repository and clock")
	}
	return &ReviewAnswerHandler{reviews: reviews, clock: clock}, nil
}

// ConfigureContinuation attaches the known-answer registry and task chain. The
// selection is always recorded; continuation only happens when the session
// carries the observed questionnaire and a vacancy block exists.
func (handler *ReviewAnswerHandler) ConfigureContinuation(resolver VacancyAnswerBlockResolver, chain VacancyTestEnqueuer) {
	if handler == nil {
		return
	}
	handler.resolver = resolver
	handler.chain = chain
}

func (handler *ReviewAnswerHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ReviewAnswerPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode review answer task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	session, err := handler.reviews.ReviewSession(ctx, payload.SessionID)
	if err != nil {
		return err
	}
	prompt, err := handler.reviews.ReviewPrompt(ctx, payload.PromptID)
	if err != nil {
		return err
	}
	if prompt.SessionID != session.ID {
		return errors.New("review prompt belongs to another session")
	}
	now := handler.clock.Now()
	selection, err := session.RecordSelection(prompt, payload.SelectedOptions, payload.Text, payload.Source, payload.ExpectedRevision, now)
	if err != nil {
		return err
	}
	if err := handler.reviews.AppendReviewSelection(ctx, session, selection, payload.ExpectedRevision); err != nil {
		return err
	}
	return handler.continueChain(ctx, session, prompt, selection, now)
}

func (handler *ReviewAnswerHandler) continueChain(ctx context.Context, session core.ReviewSession, prompt core.ReviewPrompt, selection core.ReviewSelection, now time.Time) error {
	if handler.resolver == nil || handler.chain == nil || len(session.Questionnaire.Questions) == 0 {
		return nil
	}
	block, found := handler.resolver.FindVacancy(session.Platform)
	if !found {
		return nil
	}
	externalID, ok := core.VacancyExternalIDFromTestDefinitionID(session.Platform, session.TestDefinitionID)
	if !ok {
		return nil
	}
	missing, err := core.UncoveredQuestions(session.Questionnaire, block)
	if err != nil {
		return err
	}
	remaining := make([]core.Question, 0, len(missing))
	for _, question := range missing {
		if question.ID != prompt.Question.ID {
			remaining = append(remaining, question)
		}
	}
	if len(remaining) == 0 {
		answers, err := handler.resolvedAnswers(session.Questionnaire, block, prompt.Question, selection)
		if err != nil {
			return err
		}
		requestKey := fmt.Sprintf("review:%s:%d", session.ID, session.Revision)
		_, err = handler.chain.EnqueueAnswer(ctx, session.ProfileID, session.Platform, externalID, answers, requestKey)
		return err
	}
	next := remaining[0]
	nextPrompt := core.ReviewPrompt{
		ID:        core.ReviewPromptID(fmt.Sprintf("%s-prompt-%d", session.ID, session.Revision)),
		SessionID: session.ID, Revision: session.Revision, Question: next, CreatedAt: now,
	}
	if err := session.WaitForAnswer(nextPrompt, now); err != nil && session.Status != core.ReviewUnsupported {
		return err
	}
	if session.Status != core.ReviewWaiting {
		return nil
	}
	return handler.reviews.SaveReviewPrompt(ctx, session, nextPrompt, session.Revision)
}

func (handler *ReviewAnswerHandler) resolvedAnswers(questionnaire core.Questionnaire, block core.AnswerBlock, humanQuestion core.Question, selection core.ReviewSelection) ([]core.ResolvedAnswer, error) {
	stored := make(map[string]core.StoredAnswer, len(block.Answers))
	for _, answer := range block.Answers {
		stored[core.NormalizeQuestionText(answer.Question)] = answer
	}
	answers := make([]core.ResolvedAnswer, 0, len(questionnaire.Questions))
	for _, question := range questionnaire.Questions {
		if question.ID == humanQuestion.ID {
			resolved, err := core.ResolveStoredAnswer(question, core.StoredAnswer{
				Question: question.Text, SelectedOptions: selection.SelectedOptions, Text: selection.Text,
			})
			if err != nil {
				return nil, err
			}
			answers = append(answers, resolved)
			continue
		}
		answer, exists := stored[core.NormalizeQuestionText(question.Text)]
		if !exists {
			return nil, fmt.Errorf("answer block %q has no reviewed answer for question %q", block.Tag, question.Text)
		}
		resolved, err := core.ResolveStoredAnswer(question, answer)
		if err != nil {
			return nil, err
		}
		answers = append(answers, resolved)
	}
	return answers, nil
}
