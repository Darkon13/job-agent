package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ApplicationAnswerCoveredHandler resumes vacancy questionnaires whose every
// observed question already has a reviewed answer in the platform bank. It
// never starts a new platform attempt: the stored review session provides the
// runtime questionnaire and the durable questionnaire.answer task submits it.
// Partially covered questionnaires stay in the review queue.
type ApplicationAnswerCoveredHandler struct {
	reviews storage.ReviewRepository
	blocks  AnswerBlockResolver
	chain   VacancyTestEnqueuer
	clock   Clock
}

func NewApplicationAnswerCoveredHandler(reviews storage.ReviewRepository, blocks AnswerBlockResolver, chain VacancyTestEnqueuer, clock Clock) (*ApplicationAnswerCoveredHandler, error) {
	if reviews == nil || blocks == nil || chain == nil || clock == nil {
		return nil, errors.New("application answer covered handler requires reviews, answer blocks, chain and clock")
	}
	return &ApplicationAnswerCoveredHandler{reviews: reviews, blocks: blocks, chain: chain, clock: clock}, nil
}

func (handler *ApplicationAnswerCoveredHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ApplicationAnswerCoveredPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode application answer covered payload: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("application answer covered task profile does not match payload")
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 25
	}
	sessions, err := handler.reviews.ListReviewSessions(ctx, storage.ReviewSessionFilter{
		Status: core.ReviewWaiting, ProfileID: payload.ProfileID, Platform: task.Platform, Limit: limit,
	})
	if err != nil {
		return err
	}
	enqueued := 0
	for _, session := range sessions {
		// Qualification sessions extend a family block and are replayed by
		// skill_verification.start, not by a vacancy submission.
		if session.AnswerBlockTag != "" || len(session.Questionnaire.Questions) == 0 {
			continue
		}
		externalID, ok := core.VacancyExternalIDFromTestDefinitionID(session.Platform, session.TestDefinitionID)
		if !ok {
			continue
		}
		block, found, err := handler.blocks.FindVacancy(ctx, session.Platform)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		answers, err := core.ResolveBlockAnswers(session.Questionnaire, block)
		if err != nil {
			continue
		}
		if _, err := handler.chain.EnqueueAnswer(ctx, session.ProfileID, session.Platform, externalID, answers, "bank:"+string(session.ID)); err != nil {
			return err
		}
		enqueued++
	}
	if enqueued > 0 {
		slog.Default().Info("questionnaire answers resumed from the reviewed bank",
			"profile", payload.ProfileID, "sessions", enqueued)
	}
	return nil
}
