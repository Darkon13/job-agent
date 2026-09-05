package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type fakeApplicationTransport struct {
	command         adapter.ApplicationSubmitCommand
	result          adapter.ApplicationSubmitResult
	err             error
	calls           int
	reconcileResult adapter.ApplicationReconcileResult
	reconcileErr    error
	reconcileCalls  int
	reconcileSet    bool
	vacancy         core.Vacancy
	vacancyErr      error
	vacancyCalls    int
}

type failOnceApplicationBudget struct {
	storage.ApplicationBudgetRepository
	commitFailures  int
	releaseFailures int
}

func (budget *failOnceApplicationBudget) CommitApplicationBudget(ctx context.Context, applicationID core.ApplicationID, now time.Time) error {
	if budget.commitFailures > 0 {
		budget.commitFailures--
		return errors.New("commit budget storage failure")
	}
	return budget.ApplicationBudgetRepository.CommitApplicationBudget(ctx, applicationID, now)
}

func (budget *failOnceApplicationBudget) ReleaseApplicationBudget(ctx context.Context, applicationID core.ApplicationID, now time.Time) error {
	if budget.releaseFailures > 0 {
		budget.releaseFailures--
		return errors.New("release budget storage failure")
	}
	return budget.ApplicationBudgetRepository.ReleaseApplicationBudget(ctx, applicationID, now)
}

func (transport *fakeApplicationTransport) ReconcileApplication(_ context.Context, _ adapter.ApplicationReconcileCommand) (adapter.ApplicationReconcileResult, error) {
	transport.reconcileCalls++
	if transport.reconcileErr != nil {
		return adapter.ApplicationReconcileResult{}, transport.reconcileErr
	}
	if transport.reconcileSet {
		return transport.reconcileResult, nil
	}
	return adapter.ApplicationReconcileResult{Applied: true}, nil
}

func (transport *fakeApplicationTransport) SubmitApplication(_ context.Context, command adapter.ApplicationSubmitCommand) (adapter.ApplicationSubmitResult, error) {
	transport.command = command
	transport.calls++
	if transport.err != nil {
		return adapter.ApplicationSubmitResult{}, transport.err
	}
	if transport.result.ExternalNegotiationID != "" || transport.result.AlreadyApplied {
		return transport.result, nil
	}
	return adapter.ApplicationSubmitResult{ExternalNegotiationID: "negotiation-1"}, nil
}

func (transport *fakeApplicationTransport) ReadVacancy(_ context.Context, _ core.ProfileID, key core.VacancyKey) (core.Vacancy, error) {
	transport.vacancyCalls++
	if transport.vacancyErr != nil {
		return core.Vacancy{}, transport.vacancyErr
	}
	if transport.vacancy.ExternalID != "" {
		return transport.vacancy, nil
	}
	return core.Vacancy{
		Platform: key.Platform, ExternalID: key.ExternalID, Title: "Go developer",
		State: core.VacancyStateOpen, ObservedAt: time.Date(2026, 7, 19, 14, 1, 0, 0, time.UTC),
		Attributes: map[string]any{},
	}, nil
}

func applicationFixture(t *testing.T, plans StaticApplicationPlans, transport *fakeApplicationTransport) (*ApplicationHandler, *storagememory.Repository, core.Task, *conversationClock) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	key := core.ApplicationKey{ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"}}
	application, err := core.NewApplication("application-1", key, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, _, err := repository.CreateApplication(ctx, application); err != nil {
		t.Fatalf("create application: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationSubmitPayload{ApplicationID: application.ID, Key: key})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-1", Type: core.TaskApplicationSubmit, IdempotencyKey: "application-submit-1",
		Source: "test", Platform: "hh", ProfileID: "profile-1", CorrelationID: "correlation-1", Payload: payload,
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	transports := NewApplicationTransportRegistry()
	if err := transports.Register("profile-1", transport); err != nil {
		t.Fatalf("register transport: %v", err)
	}
	clock := &conversationClock{now: now}
	handler, err := NewApplicationHandler(repository, repository, repository, transports, plans, clock)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	return handler, repository, task, clock
}

func liveApplicationPlan(resumeID string) ApplicationPlan {
	return ApplicationPlan{ResumeID: resumeID, Mode: core.ApplicationExecutionSubmit, DailyLimit: 10, Timezone: "Europe/Moscow"}
}

func TestApplicationHandlerSubmitsAndPersistsNegotiation(t *testing.T) {
	transport := &fakeApplicationTransport{}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{
		"profile-1": {ResumeID: "resume-1", Message: "Добрый день", Mode: core.ApplicationExecutionSubmit, DailyLimit: 10, Timezone: "Europe/Moscow"},
	}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle application: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationSubmitted || application.ExternalNegotiationID != "negotiation-1" || application.Attempts != 1 {
		t.Fatalf("stored application: %#v err=%v", application, err)
	}
	if transport.command.ResumeID != "resume-1" || transport.command.Message != "Добрый день" || transport.command.IdempotencyKey != task.IdempotencyKey {
		t.Fatalf("transport command: %#v", transport.command)
	}
	reservation, exists := repository.ApplicationBudget(application.ID)
	if !exists || reservation.State != core.ApplicationBudgetCommitted {
		t.Fatalf("application budget was not committed: %#v exists=%v", reservation, exists)
	}
}

func TestApplicationHandlerPersistsFullVacancyBeforeSubmission(t *testing.T) {
	transport := &fakeApplicationTransport{vacancy: core.Vacancy{
		Platform: "hh", ExternalID: "42", Title: "Senior Go developer", Employer: "Example",
		State: core.VacancyStateOpen, ObservedAt: time.Date(2026, 7, 19, 14, 1, 0, 0, time.UTC),
		Attributes: map[string]any{"description": "Build reliable services", "key_skills": []string{"Go"}},
	}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle application: %v", err)
	}
	vacancy, err := repository.Vacancy(context.Background(), core.VacancyKey{Platform: "hh", ExternalID: "42"})
	if err != nil || vacancy.Title != "Senior Go developer" || vacancy.Attributes["description"] != "Build reliable services" {
		t.Fatalf("stored vacancy=%#v err=%v", vacancy, err)
	}
}

func TestApplicationHandlerStopsBeforeSubmitForVacancyPreflight(t *testing.T) {
	for _, test := range []struct {
		name       string
		attributes map[string]any
		state      core.VacancyState
		message    string
		want       core.ApplicationStatus
	}{
		{name: "archived", state: core.VacancyStateArchived, want: core.ApplicationSkipped},
		{name: "already applied", state: core.VacancyStateOpen, attributes: map[string]any{"relations": []string{"got_response"}}, want: core.ApplicationSkipped},
		{name: "test", state: core.VacancyStateOpen, attributes: map[string]any{"has_test": true}, want: core.ApplicationWaitingValidation},
		{name: "required letter", state: core.VacancyStateOpen, attributes: map[string]any{"response_letter_required": true}, want: core.ApplicationWaitingValidation},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeApplicationTransport{vacancy: core.Vacancy{
				Platform: "hh", ExternalID: "42", Title: "Go developer", State: test.state,
				ObservedAt: time.Date(2026, 7, 19, 14, 1, 0, 0, time.UTC), Attributes: test.attributes,
			}}
			plan := liveApplicationPlan("resume-1")
			plan.Message = test.message
			handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": plan}, transport)
			if err := handler.Handle(context.Background(), task); err != nil {
				t.Fatalf("handle application: %v", err)
			}
			application, err := repository.Application(context.Background(), core.ApplicationKey{
				ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
			})
			if err != nil || application.Status != test.want || transport.calls != 0 {
				t.Fatalf("application=%#v submit calls=%d err=%v", application, transport.calls, err)
			}
		})
	}
}

func TestApplicationHandlerPersistsPermanentVacancyReadFailure(t *testing.T) {
	transport := &fakeApplicationTransport{vacancyErr: &core.OperationError{
		Category: core.ErrorPermanentFailure, Operation: "vacancies.read", Platform: "hh", Message: "vacancy is unavailable",
	}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	err := handler.Handle(context.Background(), task)
	if !core.ErrorIsCategory(err, core.ErrorPermanentFailure) {
		t.Fatalf("handle error = %v", err)
	}
	application, loadErr := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if loadErr != nil || application.Status != core.ApplicationFailed || application.FailureMessage != "vacancy is unavailable" || transport.calls != 0 {
		t.Fatalf("application=%#v submit calls=%d err=%v", application, transport.calls, loadErr)
	}
}

func TestApplicationHandlerRepairsBudgetAfterSubmittedStateWasSaved(t *testing.T) {
	transport := &fakeApplicationTransport{}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	handler.budgets = &failOnceApplicationBudget{ApplicationBudgetRepository: repository, commitFailures: 1}

	if err := handler.Handle(context.Background(), task); !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("first handle error = %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	reservation, exists := repository.ApplicationBudget(application.ID)
	if err != nil || application.Status != core.ApplicationSubmitted || !exists || reservation.State != core.ApplicationBudgetReserved {
		t.Fatalf("application=%#v reservation=%#v exists=%v err=%v", application, reservation, exists, err)
	}

	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("repair handle: %v", err)
	}
	reservation, _ = repository.ApplicationBudget(application.ID)
	if reservation.State != core.ApplicationBudgetCommitted || transport.calls != 1 {
		t.Fatalf("reservation=%#v submit calls=%d", reservation, transport.calls)
	}
}

func TestApplicationHandlerRepairsBudgetAfterFailedStateWasSaved(t *testing.T) {
	transport := &fakeApplicationTransport{err: errors.New("permanent submit failure")}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	handler.budgets = &failOnceApplicationBudget{ApplicationBudgetRepository: repository, releaseFailures: 1}

	if err := handler.Handle(context.Background(), task); !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("first handle error = %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	reservation, exists := repository.ApplicationBudget(application.ID)
	if err != nil || application.Status != core.ApplicationFailed || !exists || reservation.State != core.ApplicationBudgetReserved {
		t.Fatalf("application=%#v reservation=%#v exists=%v err=%v", application, reservation, exists, err)
	}

	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("repair handle: %v", err)
	}
	reservation, _ = repository.ApplicationBudget(application.ID)
	if reservation.State != core.ApplicationBudgetReleased || transport.calls != 1 {
		t.Fatalf("reservation=%#v submit calls=%d", reservation, transport.calls)
	}
}

func TestApplicationHandlerWaitsForMissingResume(t *testing.T) {
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{}, &fakeApplicationTransport{})
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle application: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationWaitingValidation || application.Attempts != 0 {
		t.Fatalf("stored application: %#v err=%v", application, err)
	}
}

func TestApplicationHandlerRestoresReadyStateForRetry(t *testing.T) {
	transport := &fakeApplicationTransport{err: &core.OperationError{
		Category: core.ErrorTemporaryFailure, Operation: "applications.submit", Message: "timeout",
	}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	if err := handler.Handle(context.Background(), task); !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("handler error = %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationReady || application.Attempts != 1 {
		t.Fatalf("stored application: %#v err=%v", application, err)
	}
	reservation, exists := repository.ApplicationBudget(application.ID)
	if !exists || reservation.State != core.ApplicationBudgetReleased {
		t.Fatalf("retryable failure did not release budget: %#v exists=%v", reservation, exists)
	}
}

func TestApplicationHandlerTreatsAlreadyAppliedAsIdempotentSuccess(t *testing.T) {
	transport := &fakeApplicationTransport{}
	transport.result = adapter.ApplicationSubmitResult{AlreadyApplied: true}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle duplicate application: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationSubmitted || application.ExternalNegotiationID != "" {
		t.Fatalf("stored application: %#v err=%v", application, err)
	}
}

func TestApplicationHandlerKeepsAmbiguousOutcomeForReconciliation(t *testing.T) {
	transport := &fakeApplicationTransport{err: &core.OperationError{
		Category: core.ErrorAmbiguousResult, Operation: "applications.submit", Message: "connection lost after POST",
	}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	if err := handler.Handle(context.Background(), task); !core.ErrorIsCategory(err, core.ErrorAmbiguousResult) {
		t.Fatalf("handle ambiguous application: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationPendingReconcile || application.Attempts != 1 {
		t.Fatalf("stored application: %#v err=%v", application, err)
	}
	reservation, exists := repository.ApplicationBudget(application.ID)
	if !exists || reservation.State != core.ApplicationBudgetReserved {
		t.Fatalf("ambiguous application did not retain budget: %#v exists=%v", reservation, exists)
	}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("repeated ambiguous task: %v", err)
	}
	if application, _ = repository.Application(context.Background(), application.Key); application.Attempts != 1 || application.Status != core.ApplicationSubmitted || transport.calls != 1 || transport.reconcileCalls != 1 {
		t.Fatalf("ambiguous task reconciliation failed: application=%#v submit=%d reconcile=%d", application, transport.calls, transport.reconcileCalls)
	}
	reservation, _ = repository.ApplicationBudget(application.ID)
	if reservation.State != core.ApplicationBudgetCommitted {
		t.Fatalf("reconciled application budget was not committed: %#v", reservation)
	}
}

func TestApplicationHandlerReleasesBudgetWhenReconciliationFindsNoApplication(t *testing.T) {
	transport := &fakeApplicationTransport{
		err: &core.OperationError{Category: core.ErrorAmbiguousResult, Operation: "applications.submit", Message: "lost response"},
	}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	if err := handler.Handle(context.Background(), task); !core.ErrorIsCategory(err, core.ErrorAmbiguousResult) {
		t.Fatalf("initial submit: %v", err)
	}
	transport.err = nil
	transport.reconcileResult = adapter.ApplicationReconcileResult{Applied: false}
	transport.reconcileSet = true
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	application, _ := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	reservation, _ := repository.ApplicationBudget(application.ID)
	if application.Status != core.ApplicationFailed || reservation.State != core.ApplicationBudgetReleased {
		t.Fatalf("application=%#v reservation=%#v", application, reservation)
	}
}

func TestApplicationHandlerDryRunAndApprovalNeverReachTransport(t *testing.T) {
	for _, test := range []struct {
		name string
		plan ApplicationPlan
		want core.ApplicationStatus
	}{
		{name: "dry run", plan: ApplicationPlan{Mode: core.ApplicationExecutionDryRun, Timezone: "UTC"}, want: core.ApplicationDryRun},
		{name: "approval", plan: ApplicationPlan{ResumeID: "resume-1", Mode: core.ApplicationExecutionApproval, Timezone: "UTC"}, want: core.ApplicationWaitingApproval},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &fakeApplicationTransport{}
			handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": test.plan}, transport)
			if err := handler.Handle(context.Background(), task); err != nil {
				t.Fatalf("handle: %v", err)
			}
			application, err := repository.Application(context.Background(), core.ApplicationKey{
				ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
			})
			if err != nil || application.Status != test.want || transport.calls != 0 {
				t.Fatalf("application=%#v calls=%d err=%v", application, transport.calls, err)
			}
		})
	}
}

func TestApplicationHandlerEnforcesDailyBudgetBeforeTransport(t *testing.T) {
	transport := &fakeApplicationTransport{}
	plan := liveApplicationPlan("resume-1")
	plan.DailyLimit = 1
	handler, repository, task, clock := applicationFixture(t, StaticApplicationPlans{"profile-1": plan}, transport)
	localNow := clock.Now().In(time.FixedZone("MSK", 3*60*60))
	start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location()).UTC()
	if _, err := repository.ReserveApplicationBudget(context.Background(), core.ReserveApplicationBudgetParams{
		ApplicationID: "another-application", ProfileID: "profile-1", Platform: "hh",
		WindowStart: start, WindowEnd: start.Add(24 * time.Hour), Limit: 1, Now: clock.Now(),
	}); err != nil {
		t.Fatalf("occupy budget: %v", err)
	}
	err := handler.Handle(context.Background(), task)
	if !core.ErrorIsCategory(err, core.ErrorQuotaExceeded) || transport.calls != 0 {
		t.Fatalf("handle error=%v calls=%d", err, transport.calls)
	}
}
