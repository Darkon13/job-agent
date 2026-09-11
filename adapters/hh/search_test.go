package hh

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestSearchGlobalEncodesFiltersAndNormalizesPage(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/vacancies" || request.URL.Query().Get("page") != "0" || request.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected search URL: %s", request.URL.String())
		}
		if got := request.URL.Query()["area"]; len(got) != 2 || got[0] != "1" || got[1] != "2" {
			t.Errorf("area = %#v", got)
		}
		if request.URL.Query().Get("text") != "Go developer" || request.Header.Get("Authorization") != "Bearer "+testAccessToken {
			t.Error("search query or authorization header was not propagated")
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"found":201,"page":0,"pages":3,"per_page":100,
			"items":[
				{"id":"42","name":"Go developer","alternate_url":"https://hh.ru/vacancy/42","published_at":"2026-09-05T12:30:00+0300","employer":{"id":"7","name":"Example"},"has_test":true,"salary_range":{"from":250000,"currency":"RUR"}},
				{"id":"42","name":"duplicate","published_at":"2026-09-05T12:30:00+0300"}
			]
		}`))
	}))

	page, err := client.SearchGlobal(context.Background(), SearchQuery{
		Source: SearchSourceGlobal, Text: "Go developer", Area: []string{"1", "2"},
	}, "")
	if err != nil {
		t.Fatalf("search global: %v", err)
	}
	if page.Done || page.NextCursor != "1" || len(page.Vacancies) != 1 {
		t.Fatalf("unexpected page: %#v", page)
	}
	vacancy := page.Vacancies[0]
	if vacancy.ExternalID != "42" || vacancy.Employer != "Example" || vacancy.PublishedAt == nil || vacancy.Attributes["has_test"] != true {
		t.Fatalf("unexpected vacancy: %#v", vacancy)
	}
}

func TestSearchGlobalStopsAtHHMaximumDepth(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		page, _ := strconv.Atoi(request.URL.Query().Get("page"))
		_, _ = response.Write([]byte(`{"found":5000,"page":` + strconv.Itoa(page) + `,"pages":50,"per_page":100,"items":[{"id":"42","name":"Go"}]}`))
	}))
	page, err := client.SearchGlobal(context.Background(), SearchQuery{Source: SearchSourceGlobal}, "19")
	if err != nil {
		t.Fatalf("search last accessible page: %v", err)
	}
	if !page.Done || page.NextCursor != "" {
		t.Fatalf("maximum depth did not stop pagination: %#v", page)
	}
	if _, err := client.SearchGlobal(context.Background(), SearchQuery{Source: SearchSourceGlobal}, "20"); err == nil {
		t.Fatal("expected exhausted cursor rejection")
	}
}

func TestSearchSimilarResumeUsesResumeEndpoint(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/resumes/resume-42/similar_vacancies" {
			t.Errorf("path = %q", request.URL.Path)
		}
		query := request.URL.Query()
		if query.Get("text") != "Go" || query.Get("page") != "0" || query.Get("per_page") != "50" {
			t.Errorf("query = %v", query)
		}
		if query.Has("employment_form") || query.Has("work_format") || query.Has("working_hours") || query.Has("work_schedule_by_days") {
			t.Errorf("modern work fields reached a similar endpoint: %v", query)
		}
		_, _ = response.Write([]byte(`{"found":1,"page":0,"pages":1,"per_page":50,"items":[{"id":"7","name":"Go developer","alternate_url":"https://hh.ru/vacancy/7"}]}`))
	}))
	page, err := client.SearchSimilarResume(context.Background(), SearchQuery{
		Source: SearchSourceSimilarResume, Resume: "resume-42", Text: "Go", PageSize: 50,
		WorkFormat: []string{"REMOTE"}, EmploymentForm: []string{"FULL"},
	}, "")
	if err != nil {
		t.Fatalf("search similar resume: %v", err)
	}
	if len(page.Vacancies) != 1 || page.Vacancies[0].ExternalID != "7" || !page.Done {
		t.Fatalf("unexpected page: %#v", page)
	}
}

func TestSearchSimilarVacancyUsesVacancyEndpoint(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/vacancies/42/similar_vacancies" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("area") != "1" {
			t.Errorf("query = %v", request.URL.Query())
		}
		_, _ = response.Write([]byte(`{"found":2,"page":0,"pages":2,"per_page":100,"items":[{"id":"8","name":"Backend developer"}]}`))
	}))
	page, err := client.SearchSimilarVacancy(context.Background(), SearchQuery{
		Source: SearchSourceSimilarVacancy, Vacancy: "42", Area: []string{"1"},
	}, "")
	if err != nil {
		t.Fatalf("search similar vacancy: %v", err)
	}
	if page.Done || page.NextCursor != "1" || len(page.Vacancies) != 1 {
		t.Fatalf("unexpected page: %#v", page)
	}
}

func TestSearchRelatedVacancySendsPaginationOnly(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/vacancies/42/related_vacancies" {
			t.Errorf("path = %q", request.URL.Path)
		}
		query := request.URL.Query()
		if len(query) != 2 || query.Get("page") != "0" || query.Get("per_page") != "100" {
			t.Errorf("related query = %v", query)
		}
		_, _ = response.Write([]byte(`{"found":0,"page":0,"pages":0,"per_page":100,"items":[]}`))
	}))
	page, err := client.SearchRelatedVacancy(context.Background(), SearchQuery{
		Source: SearchSourceRelatedVacancy, Vacancy: "42",
	}, "")
	if err != nil || !page.Done || len(page.Vacancies) != 0 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

func TestSearchGlobalPreservesRetryAfter(t *testing.T) {
	client := newReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Retry-After", "60")
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	started := time.Now().UTC()
	_, err := client.SearchGlobal(context.Background(), SearchQuery{Source: SearchSourceGlobal}, "")
	var operationError *core.OperationError
	if !errors.As(err, &operationError) || operationError.Category != core.ErrorRateLimited || operationError.RetryAfter == nil {
		t.Fatalf("error = %#v, want rate limit with retry_after", err)
	}
	if operationError.RetryAfter.Before(started.Add(59 * time.Second)) {
		t.Fatalf("retry_after = %s, want roughly one minute", operationError.RetryAfter)
	}
}
