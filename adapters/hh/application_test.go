package hh

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

func applicationCommand() adapter.ApplicationSubmitCommand {
	return adapter.ApplicationSubmitCommand{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
		ResumeID: "resume-1", Message: "Добрый день", IdempotencyKey: "application.submit:42",
	}
}

func TestApplicationClientSubmitsMultipartAndReadsLocation(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/negotiations" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+testAccessToken {
			t.Error("missing bearer authorization")
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if request.FormValue("resume_id") != "resume-1" || request.FormValue("vacancy_id") != "42" || request.FormValue("message") != "Добрый день" {
			t.Errorf("unexpected application form: %#v", request.MultipartForm.Value)
		}
		response.Header().Set("Location", "/negotiations/negotiation-7")
		response.WriteHeader(http.StatusCreated)
	}))

	result, err := client.SubmitApplication(context.Background(), applicationCommand())
	if err != nil || result.ExternalNegotiationID != "negotiation-7" || result.AlreadyApplied {
		t.Fatalf("submit result=%#v err=%v", result, err)
	}
}

func TestApplicationClientClassifiesHHOutcomes(t *testing.T) {
	tests := []struct {
		name           string
		status         int
		body           string
		wantCategory   core.ErrorCategory
		alreadyApplied bool
	}{
		{name: "duplicate", status: http.StatusForbidden, body: `{"errors":[{"type":"negotiations","value":"already_applied"}]}`, alreadyApplied: true},
		{name: "test", status: http.StatusForbidden, body: `{"errors":[{"type":"negotiations","value":"test_required"}]}`, wantCategory: core.ErrorValidationRequired},
		{name: "quota", status: http.StatusForbidden, body: `{"errors":[{"type":"negotiations","value":"limit_exceeded"}]}`, wantCategory: core.ErrorQuotaExceeded},
		{name: "rate limit", status: http.StatusTooManyRequests, wantCategory: core.ErrorRateLimited},
		{name: "invalid vacancy", status: http.StatusForbidden, body: `{"errors":[{"type":"negotiations","value":"invalid_vacancy"}]}`, wantCategory: core.ErrorPermanentFailure},
		{name: "unknown server outcome", status: http.StatusServiceUnavailable, wantCategory: core.ErrorAmbiguousResult},
		{name: "direct application", status: http.StatusSeeOther, wantCategory: core.ErrorUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(test.body))
			}))
			result, err := client.SubmitApplication(context.Background(), applicationCommand())
			if test.alreadyApplied {
				if err != nil || !result.AlreadyApplied {
					t.Fatalf("result=%#v err=%v", result, err)
				}
				return
			}
			if !core.ErrorIsCategory(err, test.wantCategory) {
				t.Fatalf("error=%v, want %s", err, test.wantCategory)
			}
		})
	}
}

func TestApplicationClientRejectsOversizedMessageBeforeRequest(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("oversized message reached HTTP transport")
	}))
	command := applicationCommand()
	command.Message = strings.Repeat("я", maximumApplicationMessageRunes+1)
	if _, err := client.SubmitApplication(context.Background(), command); err == nil {
		t.Fatal("expected message validation error")
	}
}

func TestApplicationClientReconcilesFromVacancyRelations(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		applied bool
	}{
		{name: "applied", body: `{"relations":["favorited","got_response"]}`, applied: true},
		{name: "not applied", body: `{"relations":["favorited"]}`, applied: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/vacancies/42" {
					t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(test.body))
			}))
			result, err := client.ReconcileApplication(context.Background(), adapter.ApplicationReconcileCommand{
				ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"}, ResumeID: "resume-1",
			})
			if err != nil || result.Applied != test.applied {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestApplicationClientRetriesInconclusiveReconciliation(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	_, err := client.ReconcileApplication(context.Background(), adapter.ApplicationReconcileCommand{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"}, ResumeID: "resume-1",
	})
	if !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("error=%v, want temporary failure", err)
	}
}
