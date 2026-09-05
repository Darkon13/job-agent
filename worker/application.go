package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type ApplicationPlan struct {
	ResumeID   string
	Message    string
	Mode       core.ApplicationExecutionMode
	DailyLimit int
	Timezone   string
}

func (plan ApplicationPlan) Validate() error {
	if err := plan.Mode.Validate(); err != nil {
		return err
	}
	if plan.Mode != core.ApplicationExecutionDryRun && plan.ResumeID == "" {
		return errors.New("application plan requires a resume outside dry-run mode")
	}
	if plan.Mode == core.ApplicationExecutionSubmit && plan.DailyLimit < 1 {
		return errors.New("live application plan requires a positive daily limit")
	}
	if plan.Timezone == "" {
		return errors.New("application plan requires a timezone")
	}
	_, err := time.LoadLocation(plan.Timezone)
	return err
}

type ApplicationPlanResolver interface {
	ResolveApplicationPlan(ctx context.Context, application core.Application) (ApplicationPlan, error)
}

type StaticApplicationPlans map[core.ProfileID]ApplicationPlan

func (plans StaticApplicationPlans) ResolveApplicationPlan(_ context.Context, application core.Application) (ApplicationPlan, error) {
	plan, exists := plans[application.Key.ProfileID]
	if !exists {
		return ApplicationPlan{}, &core.OperationError{
			Category: core.ErrorValidationRequired, Operation: "applications.prepare",
			Platform: application.Key.Vacancy.Platform, Message: "profile requires an application plan",
		}
	}
	if err := plan.Validate(); err != nil {
		return ApplicationPlan{}, &core.OperationError{
			Category: core.ErrorValidationRequired, Operation: "applications.prepare",
			Platform: application.Key.Vacancy.Platform, Message: err.Error(),
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
	budgets    storage.ApplicationBudgetRepository
	transports *ApplicationTransportRegistry
	plans      ApplicationPlanResolver
	clock      Clock
}

func NewApplicationHandler(repository storage.ApplicationRepository, budgets storage.ApplicationBudgetRepository, transports *ApplicationTransportRegistry, plans ApplicationPlanResolver, clock Clock) (*ApplicationHandler, error) {
	if repository == nil || budgets == nil || transports == nil || plans == nil || clock == nil {
		return nil, errors.New("application handler requires all dependencies")
	}
	return &ApplicationHandler{repository: repository, budgets: budgets, transports: transports, plans: plans, clock: clock}, nil
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
	if application.Status == core.ApplicationSubmitted {
		return handler.commitBudget(ctx, application, handler.clock.Now())
	}
	if application.Status == core.ApplicationPendingReconcile {
		return handler.reconcile(ctx, application)
	}
	if application.Status == core.ApplicationFailed {
		return handler.releaseBudget(ctx, application, handler.clock.Now())
	}
	if application.Status == core.ApplicationWaitingValidation {
		if err := handler.releaseBudget(ctx, application, handler.clock.Now()); err != nil {
			return err
		}
	}
	if application.Status == core.ApplicationSkipped || application.Status == core.ApplicationDryRun || application.Status == core.ApplicationWaitingApproval {
		return nil
	}
	if application.Status == core.ApplicationSubmitting {
		if err := application.Transition(core.ApplicationPendingReconcile, handler.clock.Now()); err != nil {
			return err
		}
		if err := handler.repository.SaveApplication(ctx, application, core.ApplicationSubmitting); err != nil {
			return err
		}
		return &core.OperationError{
			Category: core.ErrorAmbiguousResult, Operation: "applications.submit", Platform: application.Key.Vacancy.Platform,
			Message: "application worker restarted while submission outcome was unknown",
		}
	}
	expectedStatus := application.Status
	now := handler.clock.Now()
	if application.Status == core.ApplicationNew || application.Status == core.ApplicationWaitingValidation || application.Status == core.ApplicationWaitingApproval {
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
	switch plan.Mode {
	case core.ApplicationExecutionDryRun:
		if err := application.Transition(core.ApplicationDryRun, now); err != nil {
			return err
		}
		return handler.repository.SaveApplication(ctx, application, expectedStatus)
	case core.ApplicationExecutionApproval:
		if err := application.Transition(core.ApplicationWaitingApproval, now); err != nil {
			return err
		}
		return handler.repository.SaveApplication(ctx, application, expectedStatus)
	}
	if application.Status == core.ApplicationPreparing {
		if err := application.Transition(core.ApplicationReady, now); err != nil {
			return err
		}
	}
	if err := handler.reserveBudget(ctx, application, plan, now); err != nil {
		if saveErr := handler.repository.SaveApplication(ctx, application, expectedStatus); saveErr != nil {
			return saveErr
		}
		return err
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
	if result.ExternalNegotiationID == "" && !result.AlreadyApplied {
		return handler.finishFailure(ctx, application, errors.New("application transport returned empty negotiation id"))
	}
	application.ExternalNegotiationID = result.ExternalNegotiationID
	if err := application.Transition(core.ApplicationSubmitted, handler.clock.Now()); err != nil {
		return err
	}
	if err := handler.repository.SaveApplication(ctx, application, core.ApplicationSubmitting); err != nil {
		return err
	}
	return handler.commitBudget(ctx, application, handler.clock.Now())
}

func (handler *ApplicationHandler) finishFailure(ctx context.Context, application core.Application, cause error) error {
	now := handler.clock.Now()
	var operationError *core.OperationError
	if !errors.As(cause, &operationError) || operationError.Validate() != nil {
		operationError = &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "applications.submit", Message: cause.Error()}
	}
	switch operationError.Category {
	case core.ErrorAmbiguousResult:
		if err := application.Transition(core.ApplicationPendingReconcile, now); err != nil {
			return err
		}
		if err := handler.repository.SaveApplication(ctx, application, core.ApplicationSubmitting); err != nil {
			return err
		}
		return operationError
	case core.ErrorValidationRequired, core.ErrorConfirmationRequired:
		if err := application.Transition(core.ApplicationWaitingValidation, now); err != nil {
			return err
		}
		if err := handler.repository.SaveApplication(ctx, application, core.ApplicationSubmitting); err != nil {
			return err
		}
		return handler.releaseBudget(ctx, application, now)
	case core.ErrorTemporaryFailure, core.ErrorRateLimited, core.ErrorQuotaExceeded, core.ErrorUnauthorized:
		if err := application.Transition(core.ApplicationReady, now); err != nil {
			return err
		}
		if err := handler.repository.SaveApplication(ctx, application, core.ApplicationSubmitting); err != nil {
			return err
		}
		if err := handler.releaseBudget(ctx, application, now); err != nil {
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
		if err := handler.releaseBudget(ctx, application, now); err != nil {
			return err
		}
		return operationError
	}
}

func (handler *ApplicationHandler) reconcile(ctx context.Context, application core.Application) error {
	plan, err := handler.plans.ResolveApplicationPlan(ctx, application)
	if err != nil {
		return err
	}
	transport, err := handler.transports.Resolve(application.Key.ProfileID)
	if err != nil {
		return err
	}
	reconciler, ok := transport.(adapter.ApplicationReconciler)
	if !ok {
		return &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "applications.reconcile", Platform: application.Key.Vacancy.Platform,
			Message: "application transport cannot reconcile an ambiguous submission",
		}
	}
	result, err := reconciler.ReconcileApplication(ctx, adapter.ApplicationReconcileCommand{
		ProfileID: application.Key.ProfileID, Vacancy: application.Key.Vacancy, ResumeID: plan.ResumeID,
	})
	if err != nil {
		return err
	}
	now := handler.clock.Now()
	if result.Applied {
		application.ExternalNegotiationID = result.ExternalNegotiationID
		if err := application.Transition(core.ApplicationSubmitted, now); err != nil {
			return err
		}
		if err := handler.repository.SaveApplication(ctx, application, core.ApplicationPendingReconcile); err != nil {
			return err
		}
		return handler.commitBudget(ctx, application, now)
	}
	failure := &core.OperationError{
		Category: core.ErrorPermanentFailure, Operation: "applications.reconcile", Platform: application.Key.Vacancy.Platform,
		Message: "HH confirmed that the application was not created",
	}
	if err := application.Fail(failure, now); err != nil {
		return err
	}
	if err := handler.repository.SaveApplication(ctx, application, core.ApplicationPendingReconcile); err != nil {
		return err
	}
	return handler.releaseBudget(ctx, application, now)
}

func (handler *ApplicationHandler) reserveBudget(ctx context.Context, application core.Application, plan ApplicationPlan, now time.Time) error {
	location, err := time.LoadLocation(plan.Timezone)
	if err != nil {
		return err
	}
	localNow := now.In(location)
	localStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location)
	windowStart := localStart.UTC()
	windowEnd := localStart.AddDate(0, 0, 1).UTC()
	_, err = handler.budgets.ReserveApplicationBudget(ctx, core.ReserveApplicationBudgetParams{
		ApplicationID: application.ID, ProfileID: application.Key.ProfileID, Platform: application.Key.Vacancy.Platform,
		WindowStart: windowStart, WindowEnd: windowEnd, Limit: plan.DailyLimit, Now: now,
	})
	return normalizeBudgetError("applications.budget.reserve", application.Key.Vacancy.Platform, err)
}

func (handler *ApplicationHandler) commitBudget(ctx context.Context, application core.Application, now time.Time) error {
	err := handler.budgets.CommitApplicationBudget(ctx, application.ID, now)
	return normalizeBudgetError("applications.budget.commit", application.Key.Vacancy.Platform, err)
}

func (handler *ApplicationHandler) releaseBudget(ctx context.Context, application core.Application, now time.Time) error {
	err := handler.budgets.ReleaseApplicationBudget(ctx, application.ID, now)
	return normalizeBudgetError("applications.budget.release", application.Key.Vacancy.Platform, err)
}

func normalizeBudgetError(operation string, platform core.Platform, err error) error {
	if err == nil {
		return nil
	}
	var operationError *core.OperationError
	if errors.As(err, &operationError) && operationError.Validate() == nil {
		return operationError
	}
	return &core.OperationError{
		Category: core.ErrorTemporaryFailure, Operation: operation, Platform: platform,
		Message: "application budget storage is temporarily unavailable", Cause: err,
	}
}
