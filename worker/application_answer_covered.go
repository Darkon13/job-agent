package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ApplicationAnswerCoveredHandler resumes vacancy questionnaires whose every
// observed question already has a reviewed answer in the platform bank. It
// never starts a new platform attempt: the stored review session provides the
// runtime questionnaire and the durable questionnaire.answer task submits it.
// Partially covered questionnaires stay in the review queue.
type ApplicationAnswerCoveredHandler struct {
	reviews      storage.ReviewRepository
	blocks       AnswerBlockResolver
	chain        VacancyTestEnqueuer
	clock        Clock
	answerModels map[core.ProfileID]QualificationAnswerModel
	revisions    storage.AnswerBlockRevisionRepository
}

func NewApplicationAnswerCoveredHandler(reviews storage.ReviewRepository, blocks AnswerBlockResolver, chain VacancyTestEnqueuer, clock Clock) (*ApplicationAnswerCoveredHandler, error) {
	if reviews == nil || blocks == nil || chain == nil || clock == nil {
		return nil, errors.New("application answer covered handler requires reviews, answer blocks, chain and clock")
	}
	return &ApplicationAnswerCoveredHandler{reviews: reviews, blocks: blocks, chain: chain, clock: clock}, nil
}

// ConfigureAnswerModels enables the profile-scoped model fallback for vacancy
// questions that have no reviewed bank answer. The model still passes its local
// option/text validator before an answer is queued.
func (handler *ApplicationAnswerCoveredHandler) ConfigureAnswerModels(models map[core.ProfileID]QualificationAnswerModel) {
	if handler == nil || len(models) == 0 {
		return
	}
	handler.answerModels = models
}

// ConfigureRevisions persists model answers as an appended vacancy block
// revision. The next run can then submit them from the reviewed bank even when
// the platform popup is temporarily unavailable.
func (handler *ApplicationAnswerCoveredHandler) ConfigureRevisions(revisions storage.AnswerBlockRevisionRepository) {
	if handler == nil || revisions == nil {
		return
	}
	handler.revisions = revisions
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
	// Qualification sessions share the waiting status and are newer than the
	// vacancy backlog. Do not let the page limit hide the vacancy sessions.
	sessions, err := handler.reviews.ListReviewSessions(ctx, storage.ReviewSessionFilter{
		Status: core.ReviewWaiting, ProfileID: payload.ProfileID, Platform: task.Platform,
	})
	if err != nil {
		return err
	}
	prepared := 0
	for _, session := range sessions {
		if prepared >= limit {
			break
		}
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
		answers, err := core.ResolveBlockAnswers(session.Questionnaire, block)
		if err != nil {
			model := handler.answerModels[payload.ProfileID]
			if model == nil {
				continue
			}
			modelAnswers := make([]core.StoredAnswer, 0)
			answers, modelAnswers, err = resolveQuestionnaireWithModel(ctx, session.Platform, session.Questionnaire, block, found, model)
			if err != nil {
				continue
			}
			handler.persistModelAnswers(ctx, session, block, found, modelAnswers)
		}
		prepared++
		if payload.PrepareOnly {
			continue
		}
		if _, err := handler.chain.EnqueueAnswer(ctx, session.ProfileID, session.Platform, externalID, answers, "bank:"+string(session.ID)); err != nil {
			return err
		}
	}
	if prepared > 0 {
		slog.Default().Info("questionnaire answers prepared",
			"profile", payload.ProfileID, "sessions", prepared, "submit", !payload.PrepareOnly)
	}
	return nil
}

// resolveQuestionnaireWithModel combines reviewed answers with model answers.
// A single unresolved question leaves the whole session for a human; partial
// submissions are never sent.
func resolveQuestionnaireWithModel(ctx context.Context, platform core.Platform, questionnaire core.Questionnaire, block core.AnswerBlock, blockFound bool, model QualificationAnswerModel) ([]core.ResolvedAnswer, []core.StoredAnswer, error) {
	answers := make([]core.ResolvedAnswer, 0, len(questionnaire.Questions))
	modelAnswers := make([]core.StoredAnswer, 0)
	for _, question := range questionnaire.Questions {
		if blockFound {
			if stored, ok := storedAnswerForQuestion(block, question); ok {
				if resolved, err := core.ResolveStoredAnswer(question, stored); err == nil {
					answers = append(answers, resolved)
					continue
				}
			}
		}
		stored, err := model.Resolve(ctx, platform, question)
		if err != nil {
			return nil, nil, err
		}
		resolved, err := core.ResolveStoredAnswer(question, stored)
		if err != nil {
			return nil, nil, err
		}
		answers = append(answers, resolved)
		modelAnswers = append(modelAnswers, stored)
	}
	return answers, modelAnswers, nil
}

// persistModelAnswers stores the generated answers in the same append-only
// vacancy block humans extend. The model answers carry provenance and are not
// marked as human-reviewed; they are a local preparation for the next submit.
func (handler *ApplicationAnswerCoveredHandler) persistModelAnswers(ctx context.Context, session core.ReviewSession, block core.AnswerBlock, blockFound bool, answers []core.StoredAnswer) {
	if handler.revisions == nil || len(answers) == 0 {
		return
	}
	tag := VacancyReviewedBlockTag(session.Platform, block, blockFound)
	name := strings.TrimSpace(block.Name)
	if name == "" {
		name = "Vacancy questionnaire"
	}
	latest, exists, err := handler.revisions.LatestAnswerBlockRevision(ctx, tag)
	if err == nil && exists {
		latestDigest, digestErr := core.StoredAnswersDigest(latest.Answers)
		answersDigest, nextErr := core.StoredAnswersDigest(answers)
		if digestErr == nil && nextErr == nil && latestDigest == answersDigest {
			return
		}
	}
	if _, err := handler.revisions.AppendAnswerBlockRevision(ctx, core.AnswerBlockRevision{
		BlockTag: tag, Name: name, Kind: core.AnswerBlockVacancy, Platform: session.Platform,
		Source: "model:" + string(session.ID), Answers: answers, CreatedAt: handler.clock.Now(),
	}); err != nil {
		slog.Default().Warn("persist model questionnaire answers", "session", session.ID, "error", err)
	}
}

func storedAnswerForQuestion(block core.AnswerBlock, question core.Question) (core.StoredAnswer, bool) {
	key := core.NormalizeQuestionText(question.Text)
	for _, answer := range block.Answers {
		if core.NormalizeQuestionText(answer.Question) == key {
			return answer, true
		}
	}
	return core.StoredAnswer{}, false
}
