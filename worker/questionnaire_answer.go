package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// applicationLookup resolves one application by its platform key.
type applicationLookup interface {
	Application(ctx context.Context, key core.ApplicationKey) (core.Application, error)
}

// applicationRetryEnqueuer resets a blocked application and creates a fresh
// durable submit task after its questionnaire was filled.
type applicationRetryEnqueuer interface {
	Enqueue(ctx context.Context, applicationID core.ApplicationID, requestKey string) (core.Task, bool, error)
}

// QuestionnaireAnswerHandler submits resolved vacancy questionnaire answers
// through the bound write transport. Answers already exist when the task is
// created; this handler never guesses or generates them.
type QuestionnaireAnswerHandler struct {
	submitters   *VacancyTestSubmitterRegistry
	chain        VacancyTestEnqueuer
	applications applicationLookup
	retry        applicationRetryEnqueuer
}

func NewQuestionnaireAnswerHandler(submitters *VacancyTestSubmitterRegistry) (*QuestionnaireAnswerHandler, error) {
	if submitters == nil {
		return nil, errors.New("questionnaire answer handler requires submitter registry")
	}
	return &QuestionnaireAnswerHandler{submitters: submitters}, nil
}

// ConfigureChain attaches the follow-up recording step. A successful submit is
// recorded as a submitted attempt, which unblocks the application pipeline.
func (handler *QuestionnaireAnswerHandler) ConfigureChain(chain VacancyTestEnqueuer) {
	if handler == nil {
		return
	}
	handler.chain = chain
}

// ConfigureRetry lets a successful questionnaire submission release the
// blocked application through the ordinary retry workflow.
func (handler *QuestionnaireAnswerHandler) ConfigureRetry(applications applicationLookup, retry applicationRetryEnqueuer) {
	if handler == nil {
		return
	}
	handler.applications = applications
	handler.retry = retry
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
	key := core.VacancyKey{Platform: payload.Platform, ExternalID: payload.VacancyExternalID}
	message := ""
	if handler.applications != nil {
		if application, err := handler.applications.Application(ctx, core.ApplicationKey{ProfileID: payload.ProfileID, Vacancy: key}); err == nil {
			message = application.PreparedMessage
		}
	}
	if err := submitter.SubmitVacancyTest(ctx, payload.ProfileID, key, payload.Answers, message); err != nil {
		return err
	}
	if handler.chain != nil {
		if _, err := handler.chain.EnqueueComplete(ctx, payload.ProfileID, payload.Platform, payload.VacancyExternalID,
			core.TestAttemptSubmitted, payload.AttemptFingerprint, task.IdempotencyKey); err != nil {
			return err
		}
	}
	if handler.applications != nil && handler.retry != nil {
		key := core.ApplicationKey{
			ProfileID: payload.ProfileID,
			Vacancy:   core.VacancyKey{Platform: payload.Platform, ExternalID: payload.VacancyExternalID},
		}
		application, err := handler.applications.Application(ctx, key)
		if err == nil {
			if _, _, retryErr := handler.retry.Enqueue(ctx, application.ID, "questionnaire:"+task.IdempotencyKey); retryErr != nil {
				slog.Default().Warn("questionnaire submitted but application retry was not enqueued",
					"application", application.ID, "error", retryErr)
			}
		}
	}
	return nil
}
