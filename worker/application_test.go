package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	applicationoperator "github.com/Darkon13/job-agent/operator"
	"github.com/Darkon13/job-agent/storage"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type fakeApplicationTransport struct {
	command          adapter.ApplicationSubmitCommand
	result           adapter.ApplicationSubmitResult
	err              error
	calls            int
	reconcileResult  adapter.ApplicationReconcileResult
	reconcileErr     error
	reconcileCalls   int
	reconcileSet     bool
	reconcileCommand adapter.ApplicationReconcileCommand
	vacancy          core.Vacancy
	vacancyErr       error
	vacancyCalls     int
	suitableResumes  []adapter.SuitableResume
	suitableErr      error
	suitableCalls    int
}

type failOnceApplicationBudget struct {
	storage.ApplicationBudgetRepository
	commitFailures  int
	releaseFailures int
}

type fakeApplicationPreparer struct {
	result applicationoperator.ApplicationPreparation
	err    error
	calls  int
}

type fixedApplicationJitter struct {
	value time.Duration
}

func (source fixedApplicationJitter) Between(minimum, maximum time.Duration) time.Duration {
	if source.value != 0 {
		return source.value
	}
	return minimum
}

func (preparer *fakeApplicationPreparer) PrepareApplication(_ context.Context, _ core.Application, _ core.Vacancy) (applicationoperator.ApplicationPreparation, error) {
	preparer.calls++
	return preparer.result, preparer.err
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

func (transport *fakeApplicationTransport) ReconcileApplication(_ context.Context, command adapter.ApplicationReconcileCommand) (adapter.ApplicationReconcileResult, error) {
	transport.reconcileCalls++
	transport.reconcileCommand = command
	if transport.reconcileErr != nil {
		return adapter.ApplicationReconcileResult{}, transport.reconcileErr
	}
	if transport.reconcileSet {
		return transport.reconcileResult, nil
	}
	return adapter.ApplicationReconcileResult{Applied: true}, nil
}

func (transport *fakeApplicationTransport) ListSuitableResumes(_ context.Context, _ core.ProfileID, _ core.VacancyKey) ([]adapter.SuitableResume, error) {
	transport.suitableCalls++
	if transport.suitableErr != nil {
		return nil, transport.suitableErr
	}
	if transport.suitableResumes != nil {
		return transport.suitableResumes, nil
	}
	return []adapter.SuitableResume{{ID: "resume-1", Title: "Backend developer"}}, nil
}

func (transport *fakeApplicationTransport) SubmitApplication(_ context.Context, command adapter.ApplicationSubmitCommand) (adapter.ApplicationSubmitResult, error) {
	transport.command = command
	transport.calls++
	if transport.err != nil {
		return adapter.ApplicationSubmitResult{}, transport.err
	}
	if transport.result.ExternalNegotiationID != "" || transport.result.Applied || transport.result.AlreadyApplied {
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
	handler, err := NewApplicationHandler(repository, repository, repository, repository, repository, transports, plans, fixedApplicationJitter{}, clock)
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	return handler, repository, task, clock
}

func liveApplicationPlan(resumeID string) ApplicationPlan {
	return ApplicationPlan{
		ResumeID: resumeID, Mode: core.ApplicationExecutionSubmit, DailyLimit: 10,
		SubmitJitterMin: 15 * time.Second, SubmitJitterMax: 25 * time.Second, Timezone: "Europe/Moscow",
	}
}

func TestApplicationHandlerSubmitsAndPersistsNegotiation(t *testing.T) {
	transport := &fakeApplicationTransport{}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{
		"profile-1": {
			ResumeID: "resume-1", Message: "Добрый день", Mode: core.ApplicationExecutionSubmit, DailyLimit: 10,
			SubmitJitterMin: 15 * time.Second, SubmitJitterMax: 25 * time.Second, Timezone: "Europe/Moscow",
		},
	}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle application: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationSubmitted || application.ExternalNegotiationID != "negotiation-1" || application.Attempts != 1 || application.PreparedResumeID != "resume-1" {
		t.Fatalf("stored application: %#v err=%v", application, err)
	}
	if transport.command.ResumeID != "resume-1" || transport.command.Message != "Добрый день" || transport.command.IdempotencyKey != task.IdempotencyKey {
		t.Fatalf("transport command: %#v", transport.command)
	}
	reservation, exists := repository.ApplicationBudget(application.ID)
	if !exists || reservation.State != core.ApplicationBudgetCommitted {
		t.Fatalf("application budget was not committed: %#v exists=%v", reservation, exists)
	}
	activities, err := repository.ListProfileActivity(context.Background(), storage.ProfileActivityFilter{ProfileID: "profile-1"})
	kinds := make(map[core.ProfileActivityKind]bool, len(activities))
	for _, activity := range activities {
		kinds[activity.Kind] = true
	}
	if err != nil || len(activities) != 2 || !kinds[core.ProfileActivityApplicationSubmitted] || !kinds[core.ProfileActivityVacancyInspected] {
		t.Fatalf("application activity=%#v err=%v", activities, err)
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
			if err != nil || application.Status != test.want || application.DecisionCode == "" || application.DecisionReason == "" || application.PreparedAt == nil || transport.calls != 0 {
				t.Fatalf("application=%#v submit calls=%d err=%v", application, transport.calls, err)
			}
		})
	}
}

func TestApplicationHandlerPersistsOperatorDecisionAndMessage(t *testing.T) {
	transport := &fakeApplicationTransport{}
	provenance := core.ApplicationPreparationProvenance{
		Version: core.ApplicationPreparationProvenanceVersion, Source: core.ApplicationPreparationSourceStatic,
		OutputDigest: "sha256:27f79a25bf354e459443043d8b86aeae7c0f713e0b0832c51c8a0fd6bf4d59eb",
	}
	preparer := &fakeApplicationPreparer{result: applicationoperator.ApplicationPreparation{
		Outcome: applicationoperator.ApplicationApply, Code: "qualified", Reason: "required skill matched", Message: "Generated letter", Provenance: provenance,
	}}
	plan := liveApplicationPlan("resume-1")
	plan.Preparer = preparer
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": plan}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.DecisionCode != "qualified" || application.DecisionReason != "required skill matched" || application.PreparedMessage != "Generated letter" || application.PreparationProvenance != provenance || application.PreparedAt == nil {
		t.Fatalf("application=%#v err=%v", application, err)
	}
	if transport.command.Message != "Generated letter" || preparer.calls != 1 {
		t.Fatalf("command=%#v preparer calls=%d", transport.command, preparer.calls)
	}
}

func TestApplicationHandlerReusesPreparedMessageOnSubmitRetry(t *testing.T) {
	transport := &fakeApplicationTransport{err: &core.OperationError{
		Category: core.ErrorTemporaryFailure, Operation: "applications.submit", Message: "timeout before response",
	}}
	preparer := &fakeApplicationPreparer{result: applicationoperator.ApplicationPreparation{
		Outcome: applicationoperator.ApplicationApply, Code: "qualified", Reason: "rules passed", Message: "First stable letter",
	}}
	plan := liveApplicationPlan("resume-1")
	plan.Preparer = preparer
	plans := StaticApplicationPlans{"profile-1": plan}
	handler, _, task, clock := applicationFixture(t, plans, transport)
	if err := handler.Handle(context.Background(), task); !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("first handle: %v", err)
	}
	preparer.result.Message = "Different second letter"
	plan.ResumeID = "resume-2"
	plans["profile-1"] = plan
	transport.err = nil
	clock.now = clock.now.Add(15 * time.Second)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("retry handle: %v", err)
	}
	if preparer.calls != 1 || transport.vacancyCalls != 1 || transport.suitableCalls != 1 || transport.calls != 2 || transport.command.Message != "First stable letter" || transport.command.ResumeID != "resume-1" {
		t.Fatalf("preparer=%d vacancy=%d submit=%d command=%#v", preparer.calls, transport.vacancyCalls, transport.calls, transport.command)
	}
}

func TestApplicationHandlerAutomaticallySelectsAvailableResume(t *testing.T) {
	transport := &fakeApplicationTransport{suitableResumes: []adapter.SuitableResume{{ID: "another-resume"}}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationSubmitted || application.DecisionCode != "qualified" || application.PreparedResumeID != "another-resume" || transport.calls != 1 || transport.suitableCalls != 1 || transport.command.ResumeID != "another-resume" {
		t.Fatalf("application=%#v submit=%d suitable=%d err=%v", application, transport.calls, transport.suitableCalls, err)
	}
}

func TestApplicationHandlerWaitsWhenHHOffersNoResume(t *testing.T) {
	transport := &fakeApplicationTransport{suitableResumes: []adapter.SuitableResume{}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationWaitingValidation || application.DecisionCode != "resume_not_suitable" || application.DecisionReason != "HH has no resume available for this vacancy" || transport.calls != 0 {
		t.Fatalf("application=%#v submit=%d err=%v", application, transport.calls, err)
	}
}

func TestApplicationHandlerPersistsBrowserPreflightReview(t *testing.T) {
	transport := &fakeApplicationTransport{suitableErr: &core.OperationError{
		Category: core.ErrorValidationRequired, Operation: "applications.preflight.browser", Platform: "hh",
		Message: "HH vacancy requires a test or questionnaire", Metadata: map[string]string{"code": "questionnaire_required"},
	}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationWaitingValidation || application.DecisionCode != "questionnaire_required" || transport.calls != 0 {
		t.Fatalf("application=%#v submit=%d err=%v", application, transport.calls, err)
	}
}

func TestApplicationHandlerParksCaptchaForOperator(t *testing.T) {
	transport := &fakeApplicationTransport{err: &core.OperationError{
		Category: core.ErrorConfirmationRequired, Operation: "applications.submit.browser", Platform: "hh",
		Message: "HH требует пройти капчу перед откликом",
	}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationWaitingValidation || application.DecisionCode != "captcha_required" {
		t.Fatalf("application=%#v err=%v", application, err)
	}
}

func TestApplicationHandlerSkipsVacancyUnavailableForAccount(t *testing.T) {
	transport := &fakeApplicationTransport{vacancyErr: &core.OperationError{
		Category: core.ErrorPermanentFailure, Operation: "vacancies.read.browser", Platform: "hh",
		Message: "HH vacancy is closed or unavailable", Metadata: map[string]string{"code": "vacancy_closed"},
	}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationSkipped || application.DecisionCode != "vacancy_closed" {
		t.Fatalf("application=%#v err=%v", application, err)
	}
}

func TestApplicationHandlerAcceptsKnownSuccessWithoutNegotiationID(t *testing.T) {
	transport := &fakeApplicationTransport{result: adapter.ApplicationSubmitResult{Applied: true}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationSubmitted || application.ExternalNegotiationID != "" || transport.calls != 1 {
		t.Fatalf("application=%#v submit=%d err=%v", application, transport.calls, err)
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

func TestApplicationHandlerDefersPlatformQuotaUntilConfiguredNextDay(t *testing.T) {
	transport := &fakeApplicationTransport{err: &core.OperationError{
		Category: core.ErrorQuotaExceeded, Operation: "applications.submit", Message: "HH application quota is exhausted",
	}}
	handler, repository, task, clock := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	err := handler.Handle(context.Background(), task)
	var operationError *core.OperationError
	if !errors.As(err, &operationError) || operationError.RetryAfter == nil {
		t.Fatalf("handler error = %#v", err)
	}
	local := clock.Now().In(time.FixedZone("MSK", 3*60*60))
	wantReset := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, local.Location()).UTC()
	if !operationError.RetryAfter.Equal(wantReset) {
		t.Fatalf("retry_after=%s, want next configured day %s", operationError.RetryAfter, wantReset)
	}
	application, loadErr := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	reservation, exists := repository.ApplicationBudget(application.ID)
	if loadErr != nil || application.Status != core.ApplicationReady || !exists || reservation.State != core.ApplicationBudgetReleased {
		t.Fatalf("application=%#v budget=%#v exists=%v err=%v", application, reservation, exists, loadErr)
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
	plans := StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}
	handler, repository, task, _ := applicationFixture(t, plans, transport)
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
	plans["profile-1"] = liveApplicationPlan("resume-2")
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("repeated ambiguous task: %v", err)
	}
	if application, _ = repository.Application(context.Background(), application.Key); application.Attempts != 1 || application.Status != core.ApplicationSubmitted || transport.calls != 1 || transport.reconcileCalls != 1 || transport.reconcileCommand.ResumeID != "resume-1" {
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

func TestApplicationHandlerDryRunDoesNotResolveSuitableResume(t *testing.T) {
	transport := &fakeApplicationTransport{suitableErr: errors.New("must not be called")}
	plan := ApplicationPlan{
		ResumeID: "resume-1", Mode: core.ApplicationExecutionDryRun, Timezone: "UTC",
	}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": plan}, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle dry run: %v", err)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{
		ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || application.Status != core.ApplicationDryRun || transport.suitableCalls != 0 || transport.calls != 0 {
		t.Fatalf("application=%#v suitable=%d submit=%d err=%v", application, transport.suitableCalls, transport.calls, err)
	}
}

func TestApplicationRegistryCanExposeReadOnlyVacancyReaderWithoutSubmitTransport(t *testing.T) {
	registry := NewApplicationTransportRegistry()
	reader := &fakeApplicationTransport{}
	if err := registry.RegisterVacancyReader("profile-1", reader); err != nil {
		t.Fatalf("register vacancy reader: %v", err)
	}
	if _, err := registry.ResolveVacancyReader("profile-1"); err != nil {
		t.Fatalf("resolve vacancy reader: %v", err)
	}
	if _, err := registry.Resolve("profile-1"); err == nil {
		t.Fatal("read-only profile unexpectedly exposed submit transport")
	}
	if registry.Count() != 0 || registry.VacancyReaderCount() != 1 {
		t.Fatalf("submit=%d readers=%d", registry.Count(), registry.VacancyReaderCount())
	}
}

func TestApplicationRegistryResolvesDedicatedVacancySearcher(t *testing.T) {
	registry := NewApplicationTransportRegistry()
	reader := &fakeApplicationTransport{}
	if err := registry.RegisterVacancyReader("profile-1", reader); err != nil {
		t.Fatalf("register vacancy reader: %v", err)
	}
	if _, err := registry.ResolveVacancySearcher("profile-1"); err == nil {
		t.Fatal("a reader without search unexpectedly resolved as a searcher")
	}
	searcher := fakeVacancySearcher{}
	if err := registry.RegisterVacancySearcher("profile-1", searcher); err != nil {
		t.Fatalf("register vacancy searcher: %v", err)
	}
	resolved, err := registry.ResolveVacancySearcher("profile-1")
	if err != nil {
		t.Fatalf("resolve vacancy searcher: %v", err)
	}
	if resolved != adapter.VacancySearcher(searcher) {
		t.Fatalf("resolved searcher = %#v", resolved)
	}
}

type fakeVacancySearcher struct{}

func (fakeVacancySearcher) ValidateSearch(json.RawMessage) error { return nil }

func (fakeVacancySearcher) Search(context.Context, core.ProfileID, json.RawMessage, string) (core.SearchPage, error) {
	return core.SearchPage{}, nil
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
