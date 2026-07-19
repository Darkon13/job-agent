package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type ApplicationPlan struct {
	ResumeID string
	Message  string
}

type ApplicationPlanResolver interface {
	ResolveApplicationPlan(ctx context.Context, application core.Application) (ApplicationPlan, error)
}

type StaticApplicationPlans map[core.ProfileID]ApplicationPlan

func (plans StaticApplicationPlans) ResolveApplicationPlan(_ context.Context, application core.Application) (ApplicationPlan, error) {
	plan, exists := plans[application.Key.ProfileID]
	if !exists || plan.ResumeID == "" {
		return ApplicationPlan{}, &core.OperationError{
			Category: core.ErrorValidationRequired, Operation: "applications.prepare",
			Platform: application.Key.Vacancy.Platform, Message: "profile requires a resume id",
		}
	}
	return plan, nil
}

type ApplicationTransportRegistry struct {
	mu         sync.RWMutex
	transports map[core.ProfileID]adapter.ApplicationTransport
}

func NewApplicationTransportRegistry() *ApplicationTransportRegistry {
	return &ApplicationTransportRegistry{transports: make(map[core.ProfileID]adapter.ApplicationTransport)}
}

func (registry *ApplicationTransportRegistry) Register(profileID core.ProfileID, transport adapter.ApplicationTransport) error {
	if profileID == "" || transport == nil {
		return errors.New("application transport registration requires profile and transport")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.transports[profileID]; exists {
		return fmt.Errorf("application transport for profile %s is already registered", profileID)
	}
	registry.transports[profileID] = transport
	return nil
}

func (registry *ApplicationTransportRegistry) Resolve(profileID core.ProfileID) (adapter.ApplicationTransport, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	transport, exists := registry.transports[profileID]
	if !exists {
		return nil, &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "applications.route", Message: "no application transport for profile"}
	}
	return transport, nil
}

func (registry *ApplicationTransportRegistry) Count() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.transports)
}

type ApplicationHandler struct {
	repository storage.ApplicationRepository
	transports *ApplicationTransportRegistry
	plans      ApplicationPlanResolver
	clock      Clock
}

func NewApplicationHandler(repository storage.ApplicationRepository, transports *ApplicationTransportRegistry, plans ApplicationPlanResolver, clock Clock) (*ApplicationHandler, error) {
	if repository == nil || transports == nil || plans == nil || clock == nil {
		return nil, errors.New("application handler requires all dependencies")
	}
	return &ApplicationHandler{repository: repository, transports: transports, plans: plans, clock: clock}, nil
}

func (handler *ApplicationHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ApplicationSubmitPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode application task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	application, err := handler.repository.Application(ctx, payload.Key)
	if err != nil {
		return err
	}
	if application.ID != payload.ApplicationID {
		return errors.New("application task identity mismatch")
	}
	if application.Status == core.ApplicationSubmitted || application.Status == core.ApplicationSkipped || application.Status == core.ApplicationFailed {
		return nil
	}
	expectedStatus := application.Status
	now := handler.clock.Now()
	if application.Status == core.ApplicationNew || application.Status == core.ApplicationWaitingValidation {
		if err := application.Transition(core.ApplicationPreparing, now); err != nil {
			return err
		}
	}
	plan, err := handler.plans.ResolveApplicationPlan(ctx, application)
	if err != nil {
		if core.ErrorIsCategory(err, core.ErrorValidationRequired) && application.Status == core.ApplicationPreparing {
			if transitionErr := application.Transition(core.ApplicationWaitingValidation, now); transitionErr != nil {
				return transitionErr
			}
			return handler.repository.SaveApplication(ctx, application, expectedStatus)
		}
		return err
	}
	if application.Status == core.ApplicationPreparing {
		if err := application.Transition(core.ApplicationReady, now); err != nil {
			return err
		}
	}
	if err := application.Transition(core.ApplicationSubmitting, now); err != nil {
		return err
	}
	if err := handler.repository.SaveApplication(ctx, application, expectedStatus); err != nil {
		return err
	}
	transport, err := handler.transports.Resolve(application.Key.ProfileID)
	if err != nil {
		return handler.finishFailure(ctx, application, err)
	}
	result, err := transport.SubmitApplication(ctx, adapter.ApplicationSubmitCommand{
		ProfileID: application.Key.ProfileID, Vacancy: application.Key.Vacancy,
		ResumeID: plan.ResumeID, Message: plan.Message, IdempotencyKey: task.IdempotencyKey,
	})
	if err != nil {
		return handler.finishFailure(ctx, application, err)
	}
	if result.ExternalNegotiationID == "" {
		return handler.finishFailure(ctx, application, errors.New("application transport returned empty negotiation id"))
	}
	application.ExternalNegotiationID = result.ExternalNegotiationID
	if err := application.Transition(core.ApplicationSubmitted, handler.clock.Now()); err != nil {
		return err
	}
	return handler.repository.SaveApplication(ctx, application, core.ApplicationSubmitting)
}

func (handler *ApplicationHandler) finishFailure(ctx context.Context, application core.Application, cause error) error {
	now := handler.clock.Now()
	var operationError *core.OperationError
	if !errors.As(cause, &operationError) || operationError.Validate() != nil {
		operationError = &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "applications.submit", Message: cause.Error()}
	}
	switch operationError.Category {
	case core.ErrorValidationRequired, core.ErrorConfirmationRequired:
		if err := application.Transition(core.ApplicationWaitingValidation, now); err != nil {
			return err
		}
		return handler.repository.SaveApplication(ctx, application, core.ApplicationSubmitting)
	case core.ErrorTemporaryFailure, core.ErrorRateLimited, core.ErrorQuotaExceeded, core.ErrorUnauthorized:
		if err := application.Transition(core.ApplicationReady, now); err != nil {
			return err
		}
		if err := handler.repository.SaveApplication(ctx, application, core.ApplicationSubmitting); err != nil {
			return err
		}
		return operationError
	default:
		if err := application.Fail(operationError, now); err != nil {
			return err
		}
		if err := handler.repository.SaveApplication(ctx, application, core.ApplicationSubmitting); err != nil {
			return err
		}
		return operationError
	}
}
