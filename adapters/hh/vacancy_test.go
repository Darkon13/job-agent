package hh

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestReadVacancyLoadsApplicantFieldsAndFullDescription(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/vacancies/42" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+testAccessToken {
			t.Error("missing bearer authorization")
		}
		_, _ = response.Write([]byte(`{
			"id":"42","name":"Go developer","alternate_url":"https://hh.ru/vacancy/42",
			"published_at":"2026-09-05T12:30:00+0300","employer":{"id":"7","name":"Example"},
			"description":"<p>Build reliable services</p>","key_skills":[{"name":"Go"},{"name":"PostgreSQL"}],
			"relations":["favorited","got_response"],"allow_messages":true,
			"closed_for_applicants":false,"negotiations_url":"https://api.hh.ru/negotiations?vacancy_id=42",
			"suitable_resumes_url":"https://api.hh.ru/vacancies/42/suitable_resumes",
			"languages":[{"id":"eng","level":{"id":"b2"}}],"vacancy_properties":[{"id":"internship"}]
		}`))
	}))

	vacancy, err := client.ReadVacancy(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"})
	if err != nil {
		t.Fatalf("read vacancy: %v", err)
	}
	if vacancy.Title != "Go developer" || vacancy.Employer != "Example" || vacancy.State != core.VacancyStateOpen {
		t.Fatalf("vacancy = %#v", vacancy)
	}
	if vacancy.Attributes["description"] != "<p>Build reliable services</p>" || vacancy.Attributes["allow_messages"] != true {
		t.Fatalf("full attributes = %#v", vacancy.Attributes)
	}
	skills, ok := vacancy.Attributes["key_skills"].([]string)
	if !ok || len(skills) != 2 || skills[0] != "Go" || skills[1] != "PostgreSQL" {
		t.Fatalf("key skills = %#v", vacancy.Attributes["key_skills"])
	}
	relations, ok := vacancy.Attributes["relations"].([]string)
	if !ok || len(relations) != 2 || relations[1] != "got_response" {
		t.Fatalf("relations = %#v", vacancy.Attributes["relations"])
	}
}

func TestReadVacancyNormalizesUnavailableAndRateLimit(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		category core.ErrorCategory
	}{
		{name: "not found", status: http.StatusNotFound, category: core.ErrorPermanentFailure},
		{name: "forbidden", status: http.StatusForbidden, category: core.ErrorPermanentFailure},
		{name: "rate limited", status: http.StatusTooManyRequests, category: core.ErrorRateLimited},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Retry-After", "60")
				response.WriteHeader(test.status)
			}))
			_, err := client.ReadVacancy(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"})
			var operationError *core.OperationError
			if !errors.As(err, &operationError) || operationError.Category != test.category {
				t.Fatalf("error = %#v", err)
			}
			if test.category == core.ErrorRateLimited && (operationError.RetryAfter == nil || operationError.RetryAfter.Before(time.Now().Add(50*time.Second))) {
				t.Fatalf("retry_after = %v", operationError.RetryAfter)
			}
			if test.status == http.StatusForbidden && operationError.Metadata["code"] != "vacancy_closed" {
				t.Fatalf("metadata = %#v, want vacancy_closed code", operationError.Metadata)
			}
		})
	}
}
