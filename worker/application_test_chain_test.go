package worker

import (
	"context"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

type recordingTestChain struct {
	captures []core.VacancyKey
	profiles []core.ProfileID
	sources  []string
	err      error
}

func (chain *recordingTestChain) EnqueueCapture(_ context.Context, profileID core.ProfileID, platform core.Platform, externalID string, source string) (bool, error) {
	if chain.err != nil {
		return false, chain.err
	}
	chain.captures = append(chain.captures, core.VacancyKey{Platform: platform, ExternalID: externalID})
	chain.profiles = append(chain.profiles, profileID)
	chain.sources = append(chain.sources, source)
	return true, nil
}

func TestApplicationHandlerSchedulesTestCapture(t *testing.T) {
	transport := &fakeApplicationTransport{}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	chain := &recordingTestChain{}
	handler.ConfigureTestChain(repository, chain)
	transport.vacancy = core.Vacancy{
		Platform: "hh", ExternalID: "42", Title: "Go developer", State: core.VacancyStateOpen,
		ObservedAt: time.Date(2026, 7, 19, 14, 1, 0, 0, time.UTC),
		Attributes: map[string]any{"has_test": true},
	}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(chain.captures) != 1 || chain.captures[0].ExternalID != "42" || chain.profiles[0] != "profile-1" {
		t.Fatalf("captures = %#v", chain.captures)
	}
	if chain.sources[0] != "application-review" {
		t.Fatalf("source = %q", chain.sources[0])
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"}})
	if err != nil {
		t.Fatalf("load application: %v", err)
	}
	if application.Status != core.ApplicationWaitingValidation {
		t.Fatalf("status = %q", application.Status)
	}
}

func TestApplicationHandlerContinuesAfterSubmittedTest(t *testing.T) {
	transport := &fakeApplicationTransport{}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	chain := &recordingTestChain{}
	handler.ConfigureTestChain(repository, chain)
	transport.vacancy = core.Vacancy{
		Platform: "hh", ExternalID: "42", Title: "Go developer", State: core.VacancyStateOpen,
		ObservedAt: time.Date(2026, 7, 19, 14, 1, 0, 0, time.UTC),
		Attributes: map[string]any{"has_test": true},
	}
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	if err := repository.SaveTestAttempt(context.Background(), core.TestAttempt{
		Platform: "hh", ProfileID: "profile-1", ExternalID: "42",
		TestDefinitionID: core.VacancyTestDefinitionID("hh", "42"),
		Status:           core.TestAttemptSubmitted, Attempts: 1, ObservedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save attempt: %v", err)
	}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(chain.captures) != 0 {
		t.Fatalf("captures = %#v", chain.captures)
	}
	if transport.calls != 1 {
		t.Fatalf("submit calls = %d", transport.calls)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"}})
	if err != nil {
		t.Fatalf("load application: %v", err)
	}
	if application.Status != core.ApplicationSubmitted {
		t.Fatalf("status = %q", application.Status)
	}
}

func TestApplicationHandlerSchedulesCaptureOnQuestionnaireBlocker(t *testing.T) {
	transport := &fakeApplicationTransport{err: &core.OperationError{
		Category: core.ErrorValidationRequired, Operation: "applications.submit.browser",
		Message: "questionnaire required", Metadata: map[string]string{"code": "questionnaire_required"},
	}}
	handler, repository, task, _ := applicationFixture(t, StaticApplicationPlans{"profile-1": liveApplicationPlan("resume-1")}, transport)
	chain := &recordingTestChain{}
	handler.ConfigureTestChain(repository, chain)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(chain.captures) != 1 || chain.sources[0] != "application-submit" {
		t.Fatalf("captures = %#v sources=%#v", chain.captures, chain.sources)
	}
	application, err := repository.Application(context.Background(), core.ApplicationKey{ProfileID: "profile-1", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"}})
	if err != nil {
		t.Fatalf("load application: %v", err)
	}
	if application.Status != core.ApplicationWaitingValidation {
		t.Fatalf("status = %q", application.Status)
	}
}
