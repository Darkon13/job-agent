package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

type VacancyTestSubmitterRegistry struct {
	mu         sync.RWMutex
	submitters map[core.ProfileID]adapter.VacancyTestSubmitter
}

func NewVacancyTestSubmitterRegistry() *VacancyTestSubmitterRegistry {
	return &VacancyTestSubmitterRegistry{submitters: make(map[core.ProfileID]adapter.VacancyTestSubmitter)}
}

func (registry *VacancyTestSubmitterRegistry) Register(profileID core.ProfileID, submitter adapter.VacancyTestSubmitter) error {
	if profileID == "" || submitter == nil {
		return errors.New("vacancy test submitter registration requires profile and submitter")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.submitters[profileID]; exists {
		return fmt.Errorf("vacancy test submitter for profile %s is already registered", profileID)
	}
	registry.submitters[profileID] = submitter
	return nil
}

func (registry *VacancyTestSubmitterRegistry) Resolve(profileID core.ProfileID) (adapter.VacancyTestSubmitter, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	submitter, exists := registry.submitters[profileID]
	if !exists {
		return nil, &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "questionnaire.answer", Message: "no vacancy test submitter for profile"}
	}
	return submitter, nil
}

func (registry *VacancyTestSubmitterRegistry) Count() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.submitters)
}

// QuestionnaireAnswerHandler submits resolved vacancy questionnaire answers
// through the bound write transport. Answers already exist when the task is
// created; this handler never guesses or generates them.
type QuestionnaireAnswerHandler struct {
	submitters *VacancyTestSubmitterRegistry
}

func NewQuestionnaireAnswerHandler(submitters *VacancyTestSubmitterRegistry) (*QuestionnaireAnswerHandler, error) {
	if submitters == nil {
		return nil, errors.New("questionnaire answer handler requires submitter registry")
	}
	return &QuestionnaireAnswerHandler{submitters: submitters}, nil
}

func (handler *QuestionnaireAnswerHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.QuestionnaireAnswerPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode questionnaire answer task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("questionnaire answer task profile mismatch")
	}
	submitter, err := handler.submitters.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	return submitter.SubmitVacancyTest(ctx, payload.ProfileID, core.VacancyKey{
		Platform: payload.Platform, ExternalID: payload.VacancyExternalID,
	}, payload.Answers)
}
