package core

import (
	"testing"
	"time"
)

func TestApplicationResetForRetryClearsBlockedDecision(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	application, err := NewApplication("application-1", ApplicationKey{
		ProfileID: "primary", Vacancy: VacancyKey{Platform: "hh", ExternalID: "vacancy-1"},
	}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if err := application.Transition(ApplicationPreparing, now.Add(time.Second)); err != nil {
		t.Fatalf("preparing: %v", err)
	}
	if err := application.RecordPreparation("questionnaire_required", "vacancy requires answers", "", "", now.Add(2*time.Second)); err != nil {
		t.Fatalf("record preparation: %v", err)
	}
	if err := application.Transition(ApplicationWaitingValidation, now.Add(3*time.Second)); err != nil {
		t.Fatalf("waiting validation: %v", err)
	}
	if err := application.ResetForRetry(now.Add(4 * time.Second)); err != nil {
		t.Fatalf("reset for retry: %v", err)
	}
	if application.Status != ApplicationReady || application.DecisionCode != "" || application.DecisionReason != "" ||
		application.PreparedAt != nil || application.PreparedMessage != "" {
		t.Fatalf("reset application = %#v", application)
	}
	if err := application.ResetForRetry(now.Add(5 * time.Second)); err == nil {
		t.Fatal("expected a second reset from ready to fail")
	}
}

func TestApplicationResetForRetryClearsFailure(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	application, err := NewApplication("application-2", ApplicationKey{
		ProfileID: "primary", Vacancy: VacancyKey{Platform: "hh", ExternalID: "vacancy-2"},
	}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if err := application.Transition(ApplicationPreparing, now.Add(time.Second)); err != nil {
		t.Fatalf("preparing: %v", err)
	}
	if err := application.Transition(ApplicationReady, now.Add(2*time.Second)); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if err := application.Fail(&OperationError{Category: ErrorPermanentFailure, Operation: "applications.submit", Message: "vacancy is not accessible"}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if err := application.ResetForRetry(now.Add(4 * time.Second)); err != nil {
		t.Fatalf("reset failed application: %v", err)
	}
	if application.Status != ApplicationReady || application.FailureCategory != "" || application.FailureMessage != "" {
		t.Fatalf("reset application = %#v", application)
	}
}

func TestApplicationSkippedValidationIsRetryable(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	application, err := NewApplication("application-skip", ApplicationKey{
		ProfileID: "secondary", Vacancy: VacancyKey{Platform: "hh", ExternalID: "vacancy-skip"},
	}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if err := application.Transition(ApplicationPreparing, now.Add(time.Second)); err != nil {
		t.Fatalf("preparing: %v", err)
	}
	if err := application.Transition(ApplicationReady, now.Add(2*time.Second)); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if err := application.Transition(ApplicationSubmitting, now.Add(3*time.Second)); err != nil {
		t.Fatalf("submitting: %v", err)
	}
	if err := application.Transition(ApplicationSkipped, now.Add(4*time.Second)); err != nil {
		t.Fatalf("skipping submit attempt: %v", err)
	}
	if err := application.ResetForRetry(now.Add(5 * time.Second)); err != nil {
		t.Fatalf("reset skipped application: %v", err)
	}
	if application.Status != ApplicationReady {
		t.Fatalf("status=%s", application.Status)
	}
}
