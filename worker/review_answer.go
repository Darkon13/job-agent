package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ReviewAnswerHandler records one human selection for the current review
// prompt and, when the reviewed answers now cover the whole questionnaire,
// resumes the automatic submit chain. A stale revision never overwrites a
// newer answer.
// AnswerBlockResolver resolves reviewed blocks by vacancy platform, by tag or
// by qualification family/level.
type AnswerBlockResolver interface {
	FindVacancy(ctx context.Context, platform core.Platform) (core.AnswerBlock, bool, error)
	FindQualificationLevel(ctx context.Context, platform core.Platform, familyID, levelID string) (core.AnswerBlock, bool, error)
	Get(ctx context.Context, tag string) (core.AnswerBlock, bool, error)
}

type ReviewAnswerHandler struct {
	reviews   storage.ReviewRepository
	clock     Clock
	resolver  AnswerBlockResolver
	revisions storage.AnswerBlockRevisionRepository
	chain     VacancyTestEnqueuer
}

func NewReviewAnswerHandler(reviews storage.ReviewRepository, clock Clock) (*ReviewAnswerHandler, error) {
	if reviews == nil || clock == nil {
		return nil, errors.New("review answer handler requires review repository and clock")
	}
	return &ReviewAnswerHandler{reviews: reviews, clock: clock}, nil
}

// ConfigureContinuation attaches the known-answer registry, the append-only
// revision store and the task chain. The selection is always recorded;
// continuation only happens when the session carries the observed
// questionnaire and a vacancy block exists.
func (handler *ReviewAnswerHandler) ConfigureContinuation(resolver AnswerBlockResolver, revisions storage.AnswerBlockRevisionRepository, chain VacancyTestEnqueuer) {
	if handler == nil {
		return
	}
	handler.resolver = resolver
	handler.revisions = revisions
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
	if len(payload.Answers) != 0 {
		return handler.handleBatch(ctx, session, payload)
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
	if handler.revisions != nil {
		if err := handler.appendRevision(ctx, session, prompt, selection, now); err != nil {
			return err
		}
	}
	return handler.continueChain(ctx, session, prompt, selection, now)
}

// handleBatch records the whole questionnaire in one durable task. Answers are
// applied in question order, each advancing the session revision, and the
// submit chain continues once every question is covered.
func (handler *ReviewAnswerHandler) handleBatch(ctx context.Context, session core.ReviewSession, payload core.ReviewAnswerPayload) error {
	if len(session.Questionnaire.Questions) == 0 {
		return errors.New("review session has no observed questionnaire")
	}
	if session.Status != core.ReviewWaiting {
		return fmt.Errorf("review session is not waiting for an answer: %q", session.Status)
	}
	if payload.ExpectedRevision != session.Revision {
		return fmt.Errorf("stale review revision: got %d, current %d", payload.ExpectedRevision, session.Revision)
	}
	block, _, err := handler.reviewedBlock(ctx, session)
	if err != nil {
		return err
	}
	var missing []core.Question
	if len(block.Answers) == 0 {
		missing = append(missing, session.Questionnaire.Questions...)
	} else {
		missing, err = core.UncoveredQuestions(session.Questionnaire, block)
		if err != nil {
			return err
		}
	}
	if len(missing) == 0 {
		return errors.New("review questionnaire has no missing questions")
	}
	entries := make(map[string]core.ReviewAnswerEntry, len(payload.Answers))
	for _, entry := range payload.Answers {
		entries[strings.TrimSpace(entry.QuestionID)] = entry
	}
	if len(entries) != len(missing) {
		return errors.New("batch review answers must cover every missing question")
	}
	now := handler.clock.Now()
	selections := make([]core.ReviewSelection, 0, len(missing))
	for index, question := range missing {
		entry, ok := entries[question.ID]
		if !ok {
			return fmt.Errorf("batch review answers miss question %q", question.ID)
		}
		prompt := core.ReviewPrompt{
			ID:        core.ReviewPromptID(fmt.Sprintf("%s-prompt-%d", session.ID, session.Revision)),
			SessionID: session.ID, Revision: session.Revision, Question: question, CreatedAt: now,
		}
		if index != 0 {
			if err := session.WaitForAnswer(prompt, now); err != nil {
				return err
			}
			if err := handler.reviews.SaveReviewPrompt(ctx, session, prompt, session.Revision); err != nil {
				return err
			}
		}
		selection, err := session.RecordSelection(prompt, entry.SelectedOptions, entry.Text, payload.Source, session.Revision, now)
		if err != nil {
			return err
		}
		if err := handler.reviews.AppendReviewSelection(ctx, session, selection, selection.Revision); err != nil {
			return err
		}
		if handler.revisions != nil {
			if err := handler.appendRevision(ctx, session, prompt, selection, now); err != nil {
				return err
			}
		}
		selections = append(selections, selection)
	}
	return handler.continueBatch(ctx, session, selections, missing)
}

// continueBatch enqueues the vacancy questionnaire submit once the batch has
// covered every question.
func (handler *ReviewAnswerHandler) continueBatch(ctx context.Context, session core.ReviewSession, selections []core.ReviewSelection, missing []core.Question) error {
	if handler.resolver == nil || handler.chain == nil {
		return nil
	}
	if session.AnswerBlockTag != "" {
		return nil
	}
	externalID, ok := core.VacancyExternalIDFromTestDefinitionID(session.Platform, session.TestDefinitionID)
	if !ok {
		return nil
	}
	block, _, err := handler.reviewedBlock(ctx, session)
	if err != nil {
		return err
	}
	byQuestion := make(map[string]core.ReviewSelection, len(selections))
	for index, selection := range selections {
		byQuestion[missing[index].ID] = selection
	}
	storedByText := make(map[string]core.StoredAnswer, len(block.Answers))
	for _, answer := range block.Answers {
		storedByText[core.NormalizeQuestionText(answer.Question)] = answer
	}
	answers := make([]core.ResolvedAnswer, 0, len(session.Questionnaire.Questions))
	for _, question := range session.Questionnaire.Questions {
		if selection, exists := byQuestion[question.ID]; exists {
			resolved, err := core.ResolveStoredAnswer(question, core.StoredAnswer{
				Question: question.Text, SelectedOptions: selection.SelectedOptions, Text: selection.Text,
			})
			if err != nil {
				return err
			}
			answers = append(answers, resolved)
			continue
		}
		stored, exists := storedByText[core.NormalizeQuestionText(question.Text)]
		if !exists {
			return fmt.Errorf("answer block %q has no reviewed answer for question %q", block.Tag, question.Text)
		}
		resolved, err := core.ResolveStoredAnswer(question, stored)
		if err != nil {
			return err
		}
		answers = append(answers, resolved)
	}
	requestKey := fmt.Sprintf("review:%s:%d", session.ID, session.Revision)
	_, err = handler.chain.EnqueueAnswer(ctx, session.ProfileID, session.Platform, externalID, answers, requestKey)
	return err
}

// appendRevision extends the platform vacancy block with the human answer.
// Repeated answers with identical content do not create a new revision.
func (handler *ReviewAnswerHandler) appendRevision(ctx context.Context, session core.ReviewSession, prompt core.ReviewPrompt, selection core.ReviewSelection, now time.Time) error {
	if handler.resolver == nil {
		return nil
	}
	base, tag, err := handler.reviewedBlock(ctx, session)
	if err != nil {
		return err
	}
	if len(base.Answers) == 0 && base.Kind != core.AnswerBlockVacancy {
		return nil
	}
	name, kind := base.Name, base.Kind
	fingerprint, err := core.QuestionFingerprint(prompt.Question)
	if err != nil {
		return err
	}
	human := core.StoredAnswer{
		Question: prompt.Question.Text, QuestionFingerprint: fingerprint,
		SelectedOptions: append([]string(nil), selection.SelectedOptions...), Text: selection.Text,
	}
	answers := make([]core.StoredAnswer, 0, len(base.Answers)+1)
	replaced := false
	for _, answer := range base.Answers {
		if core.NormalizeQuestionText(answer.Question) == core.NormalizeQuestionText(human.Question) {
			answers = append(answers, human)
			replaced = true
			continue
		}
		answers = append(answers, answer)
	}
	if !replaced {
		answers = append(answers, human)
	}
	latest, exists, err := handler.revisions.LatestAnswerBlockRevision(ctx, tag)
	if err != nil {
		return err
	}
	if exists {
		latestDigest, err := core.StoredAnswersDigest(latest.Answers)
		if err != nil {
			return err
		}
		nextDigest, err := core.StoredAnswersDigest(answers)
		if err != nil {
			return err
		}
		if latestDigest == nextDigest {
			return nil
		}
	}
	_, err = handler.revisions.AppendAnswerBlockRevision(ctx, core.AnswerBlockRevision{
		BlockTag: tag, Name: name, Kind: kind, Platform: session.Platform,
		Source: "review:" + string(session.ID), Answers: answers, CreatedAt: now,
	})
	return err
}

func (handler *ReviewAnswerHandler) continueChain(ctx context.Context, session core.ReviewSession, prompt core.ReviewPrompt, selection core.ReviewSelection, now time.Time) error {
	if handler.resolver == nil || handler.chain == nil || len(session.Questionnaire.Questions) == 0 {
		return nil
	}
	// A qualification session extends its family/level block; the next
	// skill_verification.start attempt replays it, so no vacancy questionnaire
	// answer is enqueued here.
	if session.AnswerBlockTag != "" {
		return nil
	}
	block, found, err := handler.resolver.FindVacancy(ctx, session.Platform)
	if err != nil {
		return err
	}
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

// reviewedBlock returns the block human answers extend: the explicit session
// tag for qualification sessions, otherwise the platform vacancy block.
func (handler *ReviewAnswerHandler) reviewedBlock(ctx context.Context, session core.ReviewSession) (core.AnswerBlock, string, error) {
	if tag := strings.TrimSpace(session.AnswerBlockTag); tag != "" {
		block, found, err := handler.resolver.Get(ctx, tag)
		if err != nil {
			return core.AnswerBlock{}, "", err
		}
		if !found {
			return core.AnswerBlock{Tag: tag, Name: tag, Kind: core.AnswerBlockQualification, Platform: session.Platform}, tag, nil
		}
		return block, tag, nil
	}
	block, found, err := handler.resolver.FindVacancy(ctx, session.Platform)
	if err != nil {
		return core.AnswerBlock{}, "", err
	}
	if !found {
		// The first reviewed answer creates the conventional platform block;
		// later revisions extend it.
		block = core.AnswerBlock{
			Tag:  VacancyReviewedBlockTag(session.Platform, core.AnswerBlock{}, false),
			Name: "Vacancy questionnaire", Kind: core.AnswerBlockVacancy, Platform: session.Platform,
		}
	}
	return block, block.Tag, nil
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
