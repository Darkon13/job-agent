package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/buildinfo"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type runtimeRepository struct {
	stats              storage.RuntimeStats
	tasks              []storage.TaskCount
	applications       []storage.ApplicationCount
	applicationObjects []core.Application
	vacancies          map[core.VacancyKey]core.Vacancy
	campaigns          []core.ApplicationCampaign
	campaignStates     map[core.ApplicationCampaignID][]core.CampaignApplicationState
	activity           []storage.ProfileActivityCount
	activitySnapshots  []core.ProfileActivitySnapshot
	conversations      []core.Conversation
	err                error
}

func (repository *runtimeRepository) Stats(context.Context) (storage.RuntimeStats, error) {
	return repository.stats, repository.err
}

func (repository *runtimeRepository) TaskCounts(context.Context) ([]storage.TaskCount, error) {
	return repository.tasks, repository.err
}

func (repository *runtimeRepository) ApplicationCounts(context.Context) ([]storage.ApplicationCount, error) {
	return repository.applications, repository.err
}

func (repository *runtimeRepository) ApplicationByID(_ context.Context, id core.ApplicationID) (core.Application, error) {
	for _, application := range repository.applicationObjects {
		if application.ID == id {
			return application, repository.err
		}
	}
	return core.Application{}, errors.New("application not found")
}

func (repository *runtimeRepository) ListApplications(_ context.Context, filter storage.ApplicationFilter) ([]core.Application, error) {
	items := make([]core.Application, 0, len(repository.applicationObjects))
	for _, application := range repository.applicationObjects {
		if filter.ProfileID != "" && application.Key.ProfileID != filter.ProfileID {
			continue
		}
		if filter.Status != "" && application.Status != filter.Status {
			continue
		}
		items = append(items, application)
	}
	if len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, repository.err
}

func (repository *runtimeRepository) Vacancy(_ context.Context, key core.VacancyKey) (core.Vacancy, error) {
	vacancy, ok := repository.vacancies[key]
	if !ok {
		return core.Vacancy{}, errors.New("vacancy not found")
	}
	return vacancy, repository.err
}

func (repository *runtimeRepository) ListApplicationCampaigns(context.Context, int) ([]core.ApplicationCampaign, error) {
	return repository.campaigns, repository.err
}

func (repository *runtimeRepository) ListCampaignApplicationStates(_ context.Context, id core.ApplicationCampaignID) ([]core.CampaignApplicationState, error) {
	return repository.campaignStates[id], repository.err
}

func (repository *runtimeRepository) ProfileActivityCounts(context.Context, storage.ProfileActivityFilter) ([]storage.ProfileActivityCount, error) {
	return repository.activity, repository.err
}

func (repository *runtimeRepository) ListProfileActivitySnapshots(context.Context, storage.ProfileActivitySnapshotFilter) ([]core.ProfileActivitySnapshot, error) {
	return repository.activitySnapshots, repository.err
}

func (repository *runtimeRepository) ListConversations(context.Context, storage.ConversationFilter) ([]core.Conversation, error) {
	return repository.conversations, repository.err
}

func TestRuntimeAPIReportsHealthReadinessAndSummary(t *testing.T) {
	now := time.Date(2026, 9, 6, 16, 0, 0, 0, time.UTC)
	vacancy := core.Vacancy{Platform: "hh", ExternalID: "42", URL: "https://hh.ru/vacancy/42", Title: "Go developer", Employer: "Example", State: core.VacancyStateOpen, ObservedAt: now}
	application := core.Application{ID: "application-1", Key: core.ApplicationKey{ProfileID: "primary", Vacancy: vacancy.Key()}, Status: core.ApplicationSubmitted, PreparedMessage: "private cover letter", CreatedAt: now.Add(-time.Hour), UpdatedAt: now}
	repository := &runtimeRepository{
		stats:              storage.RuntimeStats{Vacancies: 12, Applications: 4, Tasks: 3, Conversations: 1},
		tasks:              []storage.TaskCount{{Type: core.TaskApplicationSubmit, Status: core.TaskNew, Priority: 90, Count: 3}},
		applications:       []storage.ApplicationCount{{Status: core.ApplicationSubmitted, Count: 4}},
		applicationObjects: []core.Application{application},
		vacancies:          map[core.VacancyKey]core.Vacancy{vacancy.Key(): vacancy},
		campaigns: []core.ApplicationCampaign{{
			ID: "campaign-1", JobTag: "daily", Status: core.ApplicationCampaignTargetReached,
			StopReason: "target successful applications reached", TargetSuccessful: 1,
			Routes: []core.SearchID{"golang"}, CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
		}},
		campaignStates: map[core.ApplicationCampaignID][]core.CampaignApplicationState{
			"campaign-1": {{Application: core.Application{Status: core.ApplicationSubmitted, DecisionCode: "qualified"}}},
		},
		activity: []storage.ProfileActivityCount{{
			Platform: "hh", ProfileID: "primary", Kind: core.ProfileActivityApplicationSubmitted,
			Count: 4, LastOccurredAt: now.Add(-time.Minute),
		}},
		activitySnapshots: []core.ProfileActivitySnapshot{{
			ID: "snapshot-1", Platform: "hh", ProfileID: "primary", ResumeID: "resume-1", SearchShows: intPointer(35), Views: intPointer(1), ScoreHidden: true, ObservedAt: now,
		}},
		conversations: []core.Conversation{{ID: "conversation-1", ProfileID: "primary", Platform: "hh", ApplicationID: application.ID, UnreadCount: 2}},
	}
	api, err := NewRuntimeAPI(repository)
	if err != nil {
		t.Fatalf("new runtime API: %v", err)
	}
	api.now = func() time.Time { return now }
	product := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	handler := api.Handler(product)

	for _, path := range []string{"/healthz", "/readyz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status: %d body=%s", path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if response.Code != http.StatusOK || !containsAll(response.Body.String(), `"version":"`+buildinfo.Current().Version+`"`, `"api_version":"v1"`, `"commit":`) {
		t.Fatalf("version response: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/summary", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("summary status: %d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control: %q", got)
	}
	want := `"generated_at":"2026-09-06T16:00:00Z"`
	if body := response.Body.String(); !containsAll(body, want, `"vacancies":12`, `"type":"application.submit"`, `"priority":90`, `"kind":"application.submitted"`, `"search_shows":35`, `"id":"conversation-1"`, `"vacancy_title":"Go developer"`, `"employer":"Example"`, `"unread_count":2`, `"id":"campaign-1"`, `"status":"target_reached"`, `"decision_code":"qualified"`) {
		t.Fatalf("unexpected summary: %s", body)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/applications?limit=20&status=submitted", nil))
	if body := response.Body.String(); response.Code != http.StatusOK || !containsAll(body, `"id":"application-1"`, `"vacancy_title":"Go developer"`, `"has_cover_letter":true`) || strings.Contains(body, "private cover letter") || strings.Contains(body, "external_negotiation_id") {
		t.Fatalf("application list response: %d %s", response.Code, body)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/other", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("fallback status: %d", response.Code)
	}
}

func intPointer(value int) *int { return &value }

func TestRuntimeAPIReadinessDoesNotLeakStorageError(t *testing.T) {
	api, err := NewRuntimeAPI(&runtimeRepository{err: errors.New("database /secret/path failed")})
	if err != nil {
		t.Fatalf("new runtime API: %v", err)
	}
	response := httptest.NewRecorder()
	api.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || containsAll(response.Body.String(), "/secret/path") {
		t.Fatalf("readiness response: %d %s", response.Code, response.Body.String())
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
