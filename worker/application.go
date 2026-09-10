package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	applicationoperator "github.com/Darkon13/job-agent/operator"
	"github.com/Darkon13/job-agent/storage"
)

type ApplicationPlan struct {
	ResumeID        string
	Message         string
	Preparer        applicationoperator.ApplicationPreparer
	Mode            core.ApplicationExecutionMode
	DailyLimit      int
	SubmitJitterMin time.Duration
	SubmitJitterMax time.Duration
	Timezone        string
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
	if plan.Mode == core.ApplicationExecutionSubmit && (plan.SubmitJitterMin <= 0 || plan.SubmitJitterMax < plan.SubmitJitterMin) {
		return errors.New("live application plan requires 0 < submit jitter min <= max")
	}
	if plan.Timezone == "" {
		return errors.New("application plan requires a timezone")
	}
	_, err := time.LoadLocation(plan.Timezone)
	return err
}

type ApplicationJitterSource interface {
	Between(minimum, maximum time.Duration) time.Duration
}

type UniformApplicationJitter struct{}

func (UniformApplicationJitter) Between(minimum, maximum time.Duration) time.Duration {
	if maximum <= minimum {
		return minimum
	}
	return minimum + time.Duration(rand.Int64N(int64(maximum-minimum)))
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
	mu                    sync.RWMutex
	transports            map[core.ProfileID]adapter.ApplicationTransport
	vacancyReaders        map[core.ProfileID]adapter.VacancyReader
	suitableResumeReaders map[core.ProfileID]adapter.SuitableResumeReader
}

func NewApplicationTransportRegistry() *ApplicationTransportRegistry {
	return &ApplicationTransportRegistry{
		transports:            make(map[core.ProfileID]adapter.ApplicationTransport),
		vacancyReaders:        make(map[core.ProfileID]adapter.VacancyReader),
		suitableResumeReaders: make(map[core.ProfileID]adapter.SuitableResumeReader),
	}
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
	if reader, ok := transport.(adapter.VacancyReader); ok {
		registry.vacancyReaders[profileID] = reader
	}
	if reader, ok := transport.(adapter.SuitableResumeReader); ok {
		registry.suitableResumeReaders[profileID] = reader
	}
	return nil
}

func (registry *ApplicationTransportRegistry) RegisterVacancyReader(profileID core.ProfileID, reader adapter.VacancyReader) error {
	if profileID == "" || reader == nil {
		return errors.New("vacancy reader registration requires profile and reader")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.vacancyReaders[profileID]; exists {
		return fmt.Errorf("vacancy reader for profile %s is already registered", profileID)
	}
	registry.vacancyReaders[profileID] = reader
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

func (registry *ApplicationTransportRegistry) ResolveVacancyReader(profileID core.ProfileID) (adapter.VacancyReader, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	reader := registry.vacancyReaders[profileID]
	if reader == nil {
		return nil, &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "vacancies.read",
			Message: "profile has no full vacancy reader",
		}
	}
	return reader, nil
}

func (registry *ApplicationTransportRegistry) ResolveSuitableResumeReader(profileID core.ProfileID) (adapter.SuitableResumeReader, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	reader := registry.suitableResumeReaders[profileID]
	if reader == nil {
		return nil, &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "vacancies.suitable_resumes",
			Message: "profile has no suitable resume reader",
		}
	}
	return reader, nil
}

func (registry *ApplicationTransportRegistry) Count() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.transports)
}

func (registry *ApplicationTransportRegistry) VacancyReaderCount() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.vacancyReaders)
}

type ApplicationHandler struct {
	repository storage.ApplicationRepository
	vacancies  storage.VacancyRepository
	budgets    storage.ApplicationBudgetRepository
	pacing     storage.ApplicationPaceRepository
	activity   storage.ProfileActivityRepository
	transports *ApplicationTransportRegistry
	plans      ApplicationPlanResolver
	jitter     ApplicationJitterSource
	clock      Clock
}

func NewApplicationHandler(repository storage.ApplicationRepository, vacancies storage.VacancyRepository, budgets storage.ApplicationBudgetRepository, pacing storage.ApplicationPaceRepository, activity storage.ProfileActivityRepository, transports *ApplicationTransportRegistry, plans ApplicationPlanResolver, jitter ApplicationJitterSource, clock Clock) (*ApplicationHandler, error) {
	if repository == nil || vacancies == nil || budgets == nil || pacing == nil || activity == nil || transports == nil || plans == nil || jitter == nil || clock == nil {
		return nil, errors.New("application handler requires all dependencies")
	}
	return &ApplicationHandler{repository: repository, vacancies: vacancies, budgets: budgets, pacing: pacing, activity: activity, transports: transports, plans: plans, jitter: jitter, clock: clock}, nil
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
		if err := handler.commitBudget(ctx, application, handler.clock.Now()); err != nil {
			return err
		}
		return handler.recordSubmitted(ctx, application)
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
		if application.PreparedAt != nil {
			return nil
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
	if application.PreparedAt == nil {
		vacancy, err := handler.loadFullVacancy(ctx, application)
		if err != nil {
			if core.ErrorIsCategory(err, core.ErrorPermanentFailure) {
				var operationError *core.OperationError
				if errors.As(err, &operationError) && operationError.Validate() == nil {
					if failErr := application.Fail(operationError, now); failErr != nil {
						return failErr
					}
					if saveErr := handler.repository.SaveApplication(ctx, application, expectedStatus); saveErr != nil {
						return saveErr
					}
				}
			}
			return err
		}
		preparedResumeID := ""
		preparation, decided := applicationPlatformPreflight(vacancy)
		if !decided && plan.Mode != core.ApplicationExecutionDryRun && plan.ResumeID != "" {
			preparation, preparedResumeID, decided, err = handler.applicationResumePreflight(ctx, application, plan.ResumeID)
			if err != nil {
				return err
			}
		}
		if !decided {
			if plan.Preparer != nil {
				preparation, err = plan.Preparer.PrepareApplication(ctx, application, vacancy)
				if err != nil {
					return err
				}
			} else {
				preparation = applicationoperator.ApplicationPreparation{
					Outcome: applicationoperator.ApplicationApply, Code: "qualified",
					Reason: "vacancy passed platform preflight", Message: plan.Message,
				}
			}
			if preparation.Outcome == applicationoperator.ApplicationApply && vacancyAttributeBool(vacancy, "response_letter_required") && strings.TrimSpace(preparation.Message) == "" {
				preparation = applicationoperator.ApplicationPreparation{
					Outcome: applicationoperator.ApplicationReview, Code: "cover_letter_required",
					Reason: "vacancy requires a non-empty cover letter",
				}
			}
		}
		if err := preparation.Validate(); err != nil {
			return &core.OperationError{
				Category: core.ErrorValidationRequired, Operation: "applications.prepare",
				Platform: application.Key.Vacancy.Platform, Message: err.Error(), Cause: err,
			}
		}
		if err := application.RecordPreparationWithProvenance(
			preparation.Code, preparation.Reason, preparedResumeID, preparation.Message, preparation.Provenance, now,
		); err != nil {
			return err
		}
		switch preparation.Outcome {
		case applicationoperator.ApplicationSkip:
			if err := application.Transition(core.ApplicationSkipped, now); err != nil {
				return err
			}
			return handler.repository.SaveApplication(ctx, application, expectedStatus)
		case applicationoperator.ApplicationReview:
			if err := application.Transition(core.ApplicationWaitingValidation, now); err != nil {
				return err
			}
			return handler.repository.SaveApplication(ctx, application, expectedStatus)
		}
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
	if err := handler.acquirePacing(ctx, application, plan, now); err != nil {
		if saveErr := handler.repository.SaveApplication(ctx, application, expectedStatus); saveErr != nil {
			return saveErr
		}
		if releaseErr := handler.releaseBudget(ctx, application, now); releaseErr != nil {
			return releaseErr
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
		ResumeID: preparedResumeID(application, plan), Message: application.PreparedMessage, IdempotencyKey: task.IdempotencyKey,
	})
	if err != nil {
		return handler.finishFailure(ctx, application, err)
	}
	if result.ExternalNegotiationID == "" && !result.Applied && !result.AlreadyApplied {
		return handler.finishFailure(ctx, application, errors.New("application transport returned empty negotiation id"))
	}
	application.ExternalNegotiationID = result.ExternalNegotiationID
	if err := application.Transition(core.ApplicationSubmitted, handler.clock.Now()); err != nil {
		return err
	}
	if err := handler.repository.SaveApplication(ctx, application, core.ApplicationSubmitting); err != nil {
		return err
	}
	if err := handler.commitBudget(ctx, application, handler.clock.Now()); err != nil {
		return err
	}
	return handler.recordSubmitted(ctx, application)
}

func (handler *ApplicationHandler) acquirePacing(ctx context.Context, application core.Application, plan ApplicationPlan, now time.Time) error {
	interval := handler.jitter.Between(plan.SubmitJitterMin, plan.SubmitJitterMax)
	reservation, allowed, err := handler.pacing.AcquireApplicationPace(ctx, core.AcquireApplicationPaceParams{
		ApplicationID: application.ID, ProfileID: application.Key.ProfileID, Platform: application.Key.Vacancy.Platform,
		Interval: interval, Now: now,
	})
	if err != nil {
		return &core.OperationError{
			Category: core.ErrorTemporaryFailure, Operation: "applications.pacing.acquire", Platform: application.Key.Vacancy.Platform,
			Message: "application pacing storage is temporarily unavailable", Cause: err,
		}
	}
	if allowed {
		return nil
	}
	retryAt := reservation.ScheduledAt
	return &core.OperationError{
		Category: core.ErrorRateLimited, Operation: "applications.pacing.wait", Platform: application.Key.Vacancy.Platform,
		RetryAfter: &retryAt, Message: "application submit is waiting for its pacing slot",
	}
}

func (handler *ApplicationHandler) applicationResumePreflight(ctx context.Context, application core.Application, resumeID string) (applicationoperator.ApplicationPreparation, string, bool, error) {
	reader, err := handler.transports.ResolveSuitableResumeReader(application.Key.ProfileID)
	if err != nil {
		return applicationoperator.ApplicationPreparation{}, "", false, err
	}
	resumes, err := reader.ListSuitableResumes(ctx, application.Key.ProfileID, application.Key.Vacancy)
	if err != nil {
		if core.ErrorIsCategory(err, core.ErrorValidationRequired) || core.ErrorIsCategory(err, core.ErrorConfirmationRequired) {
			code := "platform_validation_required"
			reason := err.Error()
			var operationError *core.OperationError
			if errors.As(err, &operationError) {
				if value := strings.TrimSpace(operationError.Metadata["code"]); value != "" {
					code = value
				}
				if strings.TrimSpace(operationError.Message) != "" {
					reason = operationError.Message
				}
			}
			return applicationoperator.ApplicationPreparation{
				Outcome: applicationoperator.ApplicationReview, Code: code, Reason: reason,
			}, "", true, nil
		}
		return applicationoperator.ApplicationPreparation{}, "", false, err
	}
	for _, resume := range resumes {
		if resume.ID == resumeID {
			return applicationoperator.ApplicationPreparation{}, resume.ID, false, nil
		}
	}
	if len(resumes) > 0 {
		return applicationoperator.ApplicationPreparation{}, resumes[0].ID, false, nil
	}
	return applicationoperator.ApplicationPreparation{
		Outcome: applicationoperator.ApplicationReview, Code: "resume_not_suitable",
		Reason: "HH has no resume available for this vacancy",
	}, "", true, nil
}

func preparedResumeID(application core.Application, plan ApplicationPlan) string {
	if application.PreparedResumeID != "" {
		return application.PreparedResumeID
	}
	return plan.ResumeID
}

func (handler *ApplicationHandler) loadFullVacancy(ctx context.Context, application core.Application) (core.Vacancy, error) {
	reader, err := handler.transports.ResolveVacancyReader(application.Key.ProfileID)
	if err != nil {
		return core.Vacancy{}, err
	}
	vacancy, err := reader.ReadVacancy(ctx, application.Key.ProfileID, application.Key.Vacancy)
	if err != nil {
		return core.Vacancy{}, err
	}
	if vacancy.Key() != application.Key.Vacancy {
		return core.Vacancy{}, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "vacancies.read", Platform: application.Key.Vacancy.Platform,
			Message: "vacancy reader returned another vacancy",
		}
	}
	if err := vacancy.Validate(); err != nil {
		return core.Vacancy{}, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "vacancies.read", Platform: application.Key.Vacancy.Platform,
			Message: "vacancy reader returned an invalid vacancy", Cause: err,
		}
	}
	if _, err := handler.vacancies.UpsertVacancy(ctx, vacancy); err != nil {
		return core.Vacancy{}, fmt.Errorf("store full vacancy %s: %w", vacancy.Key(), err)
	}
	if err := recordProfileActivity(ctx, handler.activity, vacancy.Platform, application.Key.ProfileID, "",
		core.ProfileActivityVacancyInspected, string(application.ID), vacancy.ObservedAt); err != nil {
		return core.Vacancy{}, err
	}
	return vacancy, nil
}

func applicationPlatformPreflight(vacancy core.Vacancy) (applicationoperator.ApplicationPreparation, bool) {
	if vacancy.State != core.VacancyStateOpen || vacancyAttributeBool(vacancy, "closed_for_applicants") {
		return applicationoperator.ApplicationPreparation{
			Outcome: applicationoperator.ApplicationSkip, Code: "vacancy_closed",
			Reason: "vacancy is archived or closed for applicants",
		}, true
	}
	if vacancyHasString(vacancy, "relations", "got_response") {
		return applicationoperator.ApplicationPreparation{
			Outcome: applicationoperator.ApplicationSkip, Code: "already_applied",
			Reason: "applicant relation got_response already exists",
		}, true
	}
	if vacancyAttributeBool(vacancy, "has_test") || vacancyHasNonEmptyAttribute(vacancy, "test") {
		return applicationoperator.ApplicationPreparation{
			Outcome: applicationoperator.ApplicationReview, Code: "vacancy_test_required",
			Reason: "vacancy requires a test or questionnaire",
		}, true
	}
	return applicationoperator.ApplicationPreparation{}, false
}

func vacancyAttributeBool(vacancy core.Vacancy, key string) bool {
	value, _ := vacancy.Attributes[key].(bool)
	return value
}

func vacancyHasNonEmptyAttribute(vacancy core.Vacancy, key string) bool {
	value, exists := vacancy.Attributes[key]
	if !exists || value == nil {
		return false
	}
	switch item := value.(type) {
	case string:
		return strings.TrimSpace(item) != ""
	case map[string]any:
		return len(item) != 0
	case []any:
		return len(item) != 0
	default:
		return true
	}
}

func vacancyHasString(vacancy core.Vacancy, key, expected string) bool {
	switch values := vacancy.Attributes[key].(type) {
	case []string:
		for _, value := range values {
			if value == expected {
				return true
			}
		}
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok && text == expected {
				return true
			}
		}
	}
	return false
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
		ProfileID: application.Key.ProfileID, Vacancy: application.Key.Vacancy, ResumeID: preparedResumeID(application, plan),
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
		if err := handler.commitBudget(ctx, application, now); err != nil {
			return err
		}
		return handler.recordSubmitted(ctx, application)
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

func (handler *ApplicationHandler) recordSubmitted(ctx context.Context, application core.Application) error {
	if application.SubmittedAt == nil {
		return errors.New("submitted application has no submission time")
	}
	return recordProfileActivity(ctx, handler.activity, application.Key.Vacancy.Platform, application.Key.ProfileID,
		application.PreparedResumeID, core.ProfileActivityApplicationSubmitted, string(application.ID), *application.SubmittedAt)
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
