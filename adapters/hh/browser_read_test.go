package hh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

func newBrowserReadClientFixture(t *testing.T, handler http.Handler) *BrowserReadClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := browserStorageState{Cookies: []browserCookie{{Name: "session", Value: "ready", Path: "/"}}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode browser state: %v", err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatalf("write browser state: %v", err)
	}
	client, err := NewBrowserReadClient("primary", statePath, "JobAgent/Test", server.Client())
	if err != nil {
		t.Fatalf("new browser read client: %v", err)
	}
	client.webBaseURL = server.URL
	return client
}

func TestBrowserSearchReadsBoundedVacancyCardsWithoutBearerToken(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/search/vacancy" || request.URL.Query().Get("page") != "0" || request.URL.Query().Get("items_on_page") != "2" {
			t.Errorf("unexpected browser search URL: %s", request.URL.String())
		}
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "session=ready" {
			t.Errorf("unexpected browser auth headers: Authorization=%q Cookie=%q", request.Header.Get("Authorization"), request.Header.Get("Cookie"))
		}
		_, _ = response.Write([]byte(`<html><body>
			<article data-qa="vacancy-serp__vacancy">
				<a data-qa="serp-item__title" href="/vacancy/42?from=search"><span>Go developer</span></a>
				<a data-qa="vacancy-serp__vacancy-employer">Example</a>
			</article>
			<article data-qa="vacancy-serp__vacancy">
				<a data-qa="serp-item__title" href="/vacancy/43">Backend developer</a>
			</article>
			<article data-qa="vacancy-serp__vacancy">
				<a data-qa="serp-item__title" href="/vacancy/44">Must be capped</a>
			</article>
		</body></html>`))
	}))

	page, err := client.SearchGlobal(context.Background(), SearchQuery{
		Source: SearchSourceGlobal, Text: "Go", PageSize: 2, MaxPages: 1,
	}, "")
	if err != nil {
		t.Fatalf("browser search: %v", err)
	}
	if !page.Done || page.NextCursor != "" || len(page.Vacancies) != 2 {
		t.Fatalf("unexpected bounded page: %#v", page)
	}
	if page.Vacancies[0].ExternalID != "42" || page.Vacancies[0].Title != "Go developer" || page.Vacancies[0].Employer != "Example" {
		t.Fatalf("unexpected vacancy: %#v", page.Vacancies[0])
	}
	if page.Vacancies[0].Attributes["read_channel"] != "browser" {
		t.Fatalf("browser provenance missing: %#v", page.Vacancies[0].Attributes)
	}
}

func TestBrowserReadLoadsFullVacancyTextAndSkills(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/vacancy/42" {
			t.Errorf("path = %q", request.URL.Path)
		}
		_, _ = response.Write([]byte(`<html><body>
			<h1 data-qa="vacancy-title">Go developer</h1>
			<a data-qa="vacancy-company-name">Example</a>
			<div data-qa="vacancy-description"><p>Build reliable services</p><p>Use PostgreSQL</p></div>
			<span data-qa="skills-element">Go</span><span data-qa="skills-element">PostgreSQL</span>
		</body></html>`))
	}))

	vacancy, err := client.ReadVacancy(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"})
	if err != nil {
		t.Fatalf("browser vacancy read: %v", err)
	}
	if vacancy.Title != "Go developer" || vacancy.Employer != "Example" || vacancy.State != core.VacancyStateOpen {
		t.Fatalf("unexpected vacancy: %#v", vacancy)
	}
	if vacancy.Attributes["description"] != "Build reliable services Use PostgreSQL" {
		t.Fatalf("description = %#v", vacancy.Attributes["description"])
	}
	skills, ok := vacancy.Attributes["key_skills"].([]string)
	if !ok || len(skills) != 2 || skills[1] != "PostgreSQL" {
		t.Fatalf("skills = %#v", vacancy.Attributes["key_skills"])
	}
}

func TestBrowserReadDoesNotTreatVacancyFAQAsClosedState(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<html><body>
			<h1 data-qa="vacancy-title">Go developer</h1>
			<div data-qa="vacancy-response-question_is-vacancy-open">Что делать, если вакансия закрыта?</div>
		</body></html>`))
	}))
	vacancy, err := client.ReadVacancy(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"})
	if err != nil {
		t.Fatalf("browser vacancy read: %v", err)
	}
	if vacancy.State != core.VacancyStateOpen {
		t.Fatalf("vacancy state = %s, want open", vacancy.State)
	}
}

func TestBrowserReadNormalizesRejectedSession(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusForbidden)
	}))
	_, err := client.SearchGlobal(context.Background(), SearchQuery{Source: SearchSourceGlobal}, "")
	if !core.ErrorIsCategory(err, core.ErrorUnauthorized) {
		t.Fatalf("error = %v, want unauthorized", err)
	}
}

func TestBrowserReadLoadsResumeAboutAsProfileStateWithoutMutation(t *testing.T) {
	want := "  Первая строка\nВторая & третья  "
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/resume/edit/resume-42/about" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "session=ready" {
			t.Errorf("unexpected browser auth headers: Authorization=%q Cookie=%q", request.Header.Get("Authorization"), request.Header.Get("Cookie"))
		}
		_, _ = response.Write([]byte(`<html><body><textarea data-qa="resume-editor-about">  Первая строка
Вторая &amp; третья  </textarea></body></html>`))
	}))

	observation, err := client.ReadProfileState(context.Background(), adapter.ProfileStateReadRequest{
		ProfileID: "primary", Paths: []string{"/resumes/resume-42/about"},
	})
	if err != nil {
		t.Fatalf("read profile state: %v", err)
	}
	var state struct {
		Resumes map[string]struct {
			About string `json:"about"`
		} `json:"resumes"`
	}
	if err := json.Unmarshal(observation.State, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if got := state.Resumes["resume-42"].About; got != want {
		t.Fatalf("about = %q, want %q", got, want)
	}
}

func TestBrowserReadLoadsDeclaredResumeEditorFields(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/applicant/resume" || request.URL.Query().Get("resume") != "resume-42" {
			t.Errorf("request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("X-Requested-With") != "XMLHttpRequest" || request.Header.Get("Cookie") != "session=ready" {
			t.Errorf("browser headers = %#v", request.Header)
		}
		_, _ = response.Write([]byte(`{"resume":{
			"title":[{"string":"Backend"}],
			"keySkills":[{"string":"Go"},{"string":"PostgreSQL"}],
			"experience":[{"companyName":"Example","position":"Developer","startDate":"2024-01-01","endDate":null,"description":"APIs"}],
			"lastActivityTime":"must-not-leak"
		}}`))
	}))

	observation, err := client.ReadProfileState(context.Background(), adapter.ProfileStateReadRequest{
		ProfileID: "primary",
		Paths: []string{
			"/resumes/resume-42/web/title",
			"/resumes/resume-42/web/keySkills",
			"/resumes/resume-42/web/experience",
		},
	})
	if err != nil {
		t.Fatalf("read resume editor state: %v", err)
	}
	want := `{"resumes":{"resume-42":{"web":{"experience":[{"companyName":"Example","description":"APIs","endDate":null,"position":"Developer","startDate":"2024-01-01"}],"keySkills":["Go","PostgreSQL"],"title":["Backend"]}}}}`
	if string(observation.State) != want {
		t.Fatalf("state = %s, want %s", observation.State, want)
	}
}

func TestBrowserReadLoadsDeclaredApplicantProfileFields(t *testing.T) {
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/shards/applicant/profile/get_full_data" || request.URL.Query().Get("resumeHash") != "resume-42" {
			t.Errorf("request = %s %s", request.Method, request.URL.String())
		}
		_, _ = response.Write([]byte(`{"profile":{"status":"READY","fields":{
			"firstName":[{"string":"Ivan"}],
			"area":[{"string":1}],
			"preferredWorkAreas":[{"area":1,"districts":[],"metroLines":[],"metroStations":[]}],
			"userId":[{"string":"must-not-leak"}]
		}}}`))
	}))

	observation, err := client.ReadProfileState(context.Background(), adapter.ProfileStateReadRequest{
		ProfileID: "primary",
		Paths: []string{
			"/resumes/resume-42/web_profile/firstName",
			"/resumes/resume-42/web_profile/area",
			"/resumes/resume-42/web_profile/preferredWorkAreas",
		},
	})
	if err != nil {
		t.Fatalf("read applicant profile state: %v", err)
	}
	want := `{"resumes":{"resume-42":{"web_profile":{"area":[1],"firstName":["Ivan"],"preferredWorkAreas":[{"area":1,"districts":[],"metroLines":[],"metroStations":[]}]}}}}`
	if string(observation.State) != want {
		t.Fatalf("state = %s, want %s", observation.State, want)
	}
}

func TestBrowserReadRejectsUnsupportedProfileStateBeforeRequest(t *testing.T) {
	requests := 0
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	_, err := client.ReadProfileState(context.Background(), adapter.ProfileStateReadRequest{
		ProfileID: "primary", Paths: []string{"/profile/first_name"},
	})
	if !core.ErrorIsCategory(err, core.ErrorUnsupported) || requests != 0 {
		t.Fatalf("error=%v requests=%d, want unsupported without request", err, requests)
	}
}
