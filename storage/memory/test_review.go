package memory

import (
	"context"
	"errors"
	"sort"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func (repository *Repository) UpsertTestDefinition(ctx context.Context, candidate core.TestDefinition) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := candidate.Validate(); err != nil {
		return false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.tests[candidate.ID]
	if !exists {
		repository.tests[candidate.ID] = cloneTestDefinition(candidate)
		return true, nil
	}
	if stored.Platform != candidate.Platform || stored.ExternalID != candidate.ExternalID || qualificationIdentity(stored.Qualification) != qualificationIdentity(candidate.Qualification) {
		return false, errors.New("test definition id conflicts with different platform metadata")
	}
	if candidate.UpdatedAt.Before(stored.UpdatedAt) {
		return false, nil
	}
	merged := cloneTestDefinition(candidate)
	questions := make(map[string]core.TestQuestion, len(stored.Questions)+len(candidate.Questions))
	for _, question := range stored.Questions {
		questions[question.Fingerprint] = question
	}
	for _, question := range candidate.Questions {
		if previous, ok := questions[question.Fingerprint]; ok && previous.FirstSeenAt.Before(question.FirstSeenAt) {
			question.FirstSeenAt = previous.FirstSeenAt
		}
		if previous, ok := questions[question.Fingerprint]; ok && previous.LastSeenAt.After(question.LastSeenAt) {
			question.LastSeenAt = previous.LastSeenAt
		}
		questions[question.Fingerprint] = question
	}
	merged.Questions = make([]core.TestQuestion, 0, len(questions))
	for _, question := range questions {
		merged.Questions = append(merged.Questions, cloneTestQuestion(question))
	}
	sort.Slice(merged.Questions, func(i, j int) bool { return merged.Questions[i].Fingerprint < merged.Questions[j].Fingerprint })
	if stored.ObservedAttempts > merged.ObservedAttempts {
		merged.ObservedAttempts = stored.ObservedAttempts
		merged.LastAttemptFingerprint = stored.LastAttemptFingerprint
	} else if merged.LastAttemptFingerprint == "" {
		merged.LastAttemptFingerprint = stored.LastAttemptFingerprint
	}
	repository.tests[candidate.ID] = merged
	return false, nil
}

func (repository *Repository) TestDefinition(ctx context.Context, id core.TestDefinitionID) (core.TestDefinition, error) {
	if err := ctx.Err(); err != nil {
		return core.TestDefinition{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	definition, exists := repository.tests[id]
	if !exists {
		return core.TestDefinition{}, errors.New("test definition not found")
	}
	return cloneTestDefinition(definition), nil
}

func (repository *Repository) ListTestDefinitions(ctx context.Context, filter storage.TestDefinitionFilter) ([]core.TestDefinition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.TestDefinition, 0, len(repository.tests))
	for _, definition := range repository.tests {
		if filter.Platform != "" && definition.Platform != filter.Platform {
			continue
		}
		if filter.FamilyID != "" && (definition.Qualification == nil || definition.Qualification.FamilyID != filter.FamilyID) {
			continue
		}
		if filter.LevelID != "" && (definition.Qualification == nil || definition.Qualification.LevelID != filter.LevelID) {
			continue
		}
		result = append(result, cloneTestDefinition(definition))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (repository *Repository) CreateReviewSession(ctx context.Context, candidate core.ReviewSession) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := candidate.Validate(); err != nil {
		return false, err
	}
	if candidate.Status != core.ReviewPending || candidate.Revision != 1 {
		return false, errors.New("review repository accepts only initialized pending sessions")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.reviews[candidate.ID]
	if exists {
		if stored.TestDefinitionID != candidate.TestDefinitionID || stored.ProfileID != candidate.ProfileID || stored.CorrelationID != candidate.CorrelationID {
			return false, errors.New("review session id conflicts with a different session")
		}
		return false, nil
	}
	if _, exists := repository.tests[candidate.TestDefinitionID]; !exists {
		return false, errors.New("review session references unknown test definition")
	}
	repository.reviews[candidate.ID] = cloneReviewSession(candidate)
	return true, nil
}

func (repository *Repository) ReviewSession(ctx context.Context, id core.ReviewSessionID) (core.ReviewSession, error) {
	if err := ctx.Err(); err != nil {
		return core.ReviewSession{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	session, exists := repository.reviews[id]
	if !exists {
		return core.ReviewSession{}, errors.New("review session not found")
	}
	return cloneReviewSession(session), nil
}

func (repository *Repository) ListReviewSessions(ctx context.Context, filter storage.ReviewSessionFilter) ([]core.ReviewSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.ReviewSession, 0, len(repository.reviews))
	for _, session := range repository.reviews {
		if filter.Status != "" && session.Status != filter.Status ||
			filter.ProfileID != "" && session.ProfileID != filter.ProfileID ||
			filter.Platform != "" && session.Platform != filter.Platform {
			continue
		}
		result = append(result, cloneReviewSession(session))
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].UpdatedAt.After(result[j].UpdatedAt)
		}
		return result[i].ID < result[j].ID
	})
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (repository *Repository) ReviewPrompt(ctx context.Context, id core.ReviewPromptID) (core.ReviewPrompt, error) {
	if err := ctx.Err(); err != nil {
		return core.ReviewPrompt{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	prompt, exists := repository.prompts[id]
	if !exists {
		return core.ReviewPrompt{}, errors.New("review prompt not found")
	}
	return cloneReviewPrompt(prompt), nil
}

func (repository *Repository) SaveReviewPrompt(ctx context.Context, session core.ReviewSession, prompt core.ReviewPrompt, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.Validate(); err != nil {
		return err
	}
	if err := prompt.Validate(); err != nil {
		return err
	}
	if session.Status != core.ReviewWaiting || session.Revision != expectedRevision || prompt.SessionID != session.ID || prompt.Revision != expectedRevision {
		return errors.New("review prompt does not match waiting session revision")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.reviews[session.ID]
	if !exists || stored.Revision != expectedRevision || (stored.Status != core.ReviewPending && stored.Status != core.ReviewAnswered) {
		return storage.ErrRevisionConflict
	}
	if _, exists := repository.prompts[prompt.ID]; exists {
		return storage.ErrRevisionConflict
	}
	repository.prompts[prompt.ID] = cloneReviewPrompt(prompt)
	repository.reviews[session.ID] = cloneReviewSession(session)
	return nil
}

func (repository *Repository) AppendReviewSelection(ctx context.Context, session core.ReviewSession, selection core.ReviewSelection, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.Validate(); err != nil {
		return err
	}
	if err := selection.Validate(); err != nil {
		return err
	}
	if session.Status != core.ReviewAnswered || session.Revision != expectedRevision+1 || selection.SessionID != session.ID || selection.Revision != expectedRevision {
		return errors.New("review selection does not match answered session revision")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.reviews[session.ID]
	prompt, promptExists := repository.prompts[selection.PromptID]
	if !exists || stored.Revision != expectedRevision || stored.Status != core.ReviewWaiting || !promptExists || prompt.SessionID != session.ID || prompt.Revision != expectedRevision {
		return storage.ErrRevisionConflict
	}
	repository.selections[session.ID] = append(repository.selections[session.ID], cloneReviewSelection(selection))
	repository.reviews[session.ID] = session
	return nil
}

func (repository *Repository) ReviewSelections(ctx context.Context, sessionID core.ReviewSessionID) ([]core.ReviewSelection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	stored := repository.selections[sessionID]
	result := make([]core.ReviewSelection, len(stored))
	for index, selection := range stored {
		result[index] = cloneReviewSelection(selection)
	}
	return result, nil
}

func cloneTestDefinition(source core.TestDefinition) core.TestDefinition {
	result := source
	if source.Qualification != nil {
		qualification := *source.Qualification
		if source.Qualification.LevelOrder != nil {
			order := *source.Qualification.LevelOrder
			qualification.LevelOrder = &order
		}
		result.Qualification = &qualification
	}
	result.Questions = make([]core.TestQuestion, len(source.Questions))
	for index, question := range source.Questions {
		result.Questions[index] = cloneTestQuestion(question)
	}
	return result
}

func cloneTestQuestion(source core.TestQuestion) core.TestQuestion {
	result := source
	result.Options = append([]core.TestOption(nil), source.Options...)
	return result
}

func cloneReviewPrompt(source core.ReviewPrompt) core.ReviewPrompt {
	result := source
	result.Question.Options = append([]core.QuestionOption(nil), source.Question.Options...)
	if source.Deadline != nil {
		deadline := *source.Deadline
		result.Deadline = &deadline
	}
	return result
}

func cloneReviewSelection(source core.ReviewSelection) core.ReviewSelection {
	result := source
	result.SelectedOptions = append([]string(nil), source.SelectedOptions...)
	return result
}

func cloneReviewSession(source core.ReviewSession) core.ReviewSession {
	result := source
	if len(source.Questionnaire.Questions) != 0 {
		result.Questionnaire = core.Questionnaire{Title: source.Questionnaire.Title, Questions: make([]core.Question, len(source.Questionnaire.Questions))}
		for index, question := range source.Questionnaire.Questions {
			result.Questionnaire.Questions[index] = question
			result.Questionnaire.Questions[index].Options = append([]core.QuestionOption(nil), question.Options...)
		}
	}
	return result
}

func qualificationIdentity(value *core.QualificationDescriptor) string {
	if value == nil {
		return ""
	}
	return value.FamilyID + "\x00" + value.LevelID
}
