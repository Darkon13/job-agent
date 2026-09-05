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
