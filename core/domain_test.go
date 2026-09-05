package core

import (
	"errors"
	"testing"
	"time"
)

func TestCapabilitySetIsDeduplicatedAndSorted(t *testing.T) {
	set, err := NewCapabilitySet(CapabilityApply, CapabilityGetVacancy, CapabilityApply)
	if err != nil {
		t.Fatalf("new capability set: %v", err)
	}
	if !set.Supports(CapabilityApply) || set.Supports(CapabilityResumePublish) {
		t.Fatalf("unexpected capability support: %#v", set)
	}
	got := set.Sorted()
	if len(got) != 2 || got[0] != CapabilityApply || got[1] != CapabilityGetVacancy {
		t.Fatalf("unexpected sorted capabilities: %#v", got)
	}
}

func TestOperationErrorFallbackOnlyForUnsupported(t *testing.T) {
	unsupported := &OperationError{Category: ErrorUnsupported, Operation: "vacancies.search", Message: "API filter is unavailable"}
	if err := unsupported.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !AllowsTransportFallback(unsupported) {
		t.Fatal("unsupported error must allow transport fallback")
	}
	temporary := &OperationError{Category: ErrorTemporaryFailure, Operation: "vacancies.search", Cause: errors.New("timeout")}
	if AllowsTransportFallback(temporary) {
		t.Fatal("temporary error must not allow transport fallback")
	}
}

func TestQuotaExceededMayExposeResetWithoutTransportFallback(t *testing.T) {
	resetAt := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	err := &OperationError{
		Category: ErrorQuotaExceeded, Operation: "applications.apply",
		RetryAfter: &resetAt, Message: "daily application quota exhausted",
	}
	if validationError := err.Validate(); validationError != nil {
		t.Fatalf("validate quota error: %v", validationError)
	}
	if AllowsTransportFallback(err) {
		t.Fatal("quota exhaustion must not switch transport")
	}
}

func TestSearchPageRejectsDuplicateVacancy(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	vacancy := Vacancy{Platform: "hh", ExternalID: "42", Title: "Go developer", State: VacancyStateOpen, ObservedAt: now}
	page := SearchPage{Vacancies: []Vacancy{vacancy, vacancy}}
	if err := page.Validate(); err == nil {
		t.Fatal("expected duplicate vacancy error")
	}
}

func TestApplicationLifecycleAndRetry(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	application, err := NewApplication("application-1", ApplicationKey{
		ProfileID: "profile-1",
		Vacancy:   VacancyKey{Platform: "hh", ExternalID: "42"},
	}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	for index, status := range []ApplicationStatus{
		ApplicationPreparing,
		ApplicationReady,
		ApplicationSubmitting,
		ApplicationReady,
		ApplicationSubmitting,
		ApplicationSubmitted,
	} {
		if err := application.Transition(status, now.Add(time.Duration(index+1)*time.Minute)); err != nil {
			t.Fatalf("transition to %s: %v", status, err)
		}
	}
	if application.Attempts != 2 || application.SubmittedAt == nil {
		t.Fatalf("unexpected submitted application: %#v", application)
	}
	if err := application.Transition(ApplicationReady, now.Add(10*time.Minute)); err == nil {
		t.Fatal("expected terminal application transition to fail")
	}
}

func TestApplicationPreparationIsRecordedBeforeExternalAction(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	application, err := NewApplication("application-1", ApplicationKey{
		ProfileID: "profile-1", Vacancy: VacancyKey{Platform: "hh", ExternalID: "42"},
	}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if err := application.Transition(ApplicationPreparing, now.Add(time.Minute)); err != nil {
		t.Fatalf("prepare transition: %v", err)
	}
	preparedAt := now.Add(2 * time.Minute)
	if err := application.RecordPreparation("qualified", "rules passed", "  Hello  ", preparedAt); err != nil {
		t.Fatalf("record preparation: %v", err)
	}
	if application.DecisionCode != "qualified" || application.DecisionReason != "rules passed" || application.PreparedMessage != "Hello" || application.PreparedAt == nil || !application.PreparedAt.Equal(preparedAt) {
		t.Fatalf("application = %#v", application)
	}
}

func TestProfileIsAccountNotProcess(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	profile, err := NewProfile("profile-1", "hh-main", "hh", now)
	if err != nil {
		t.Fatalf("new profile: %v", err)
	}
	if err := profile.SetStatus(ProfileAuthRequired, now.Add(time.Minute)); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if profile.Status != ProfileAuthRequired {
		t.Fatalf("unexpected profile status: %s", profile.Status)
	}
}
