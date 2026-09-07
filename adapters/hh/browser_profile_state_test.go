package hh

import (
	"context"
	"encoding/json"
	stdhtml "html"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func browserProfileStateProposal(t *testing.T, before, desired string) core.ProfileStateProposal {
	t.Helper()
	now := time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-42":{"about":`+desired+`}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	observation, err := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-42":{"about":`+before+`}}}`), "", now)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	proposal, err := core.NewProfileStateProposal("proposal-1", resource, observation, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	return proposal
}

func newBrowserProfileStateClientFixture(t *testing.T, handler http.Handler) *BrowserProfileStateClient {
	t.Helper()
	reader := newBrowserReadClientFixture(t, handler)
	state := browserStorageState{Cookies: []browserCookie{
		{Name: "session", Value: "ready", Path: "/"},
		{Name: "_xsrf", Value: "csrf-token", Path: "/"},
	}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode browser state: %v", err)
	}
	if err := os.WriteFile(reader.stateFile, data, 0o600); err != nil {
		t.Fatalf("write browser state: %v", err)
	}
	return newBrowserProfileStateClient(reader)
}

func TestBrowserProfileStateAppliesAboutAndDeduplicatesRetry(t *testing.T) {
	current := "old"
	gets, posts := 0, 0
	client := newBrowserProfileStateClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			gets++
			if request.URL.Path != "/resume/edit/resume-42/about" {
				t.Errorf("GET path = %q", request.URL.Path)
			}
			_, _ = response.Write([]byte(`<textarea data-qa="resume-editor-about">` + stdhtml.EscapeString(current) + `</textarea>`))
		case http.MethodPost:
			posts++
			if request.URL.Path != "/applicant/resume/edit" || request.URL.Query().Get("resume") != "resume-42" || request.URL.Query().Get("hhtmSource") != "profile-state" {
				t.Errorf("POST URL = %s", request.URL.String())
			}
			if request.Header.Get("X-Xsrftoken") != "csrf-token" || request.Header.Get("X-Requested-With") != "XMLHttpRequest" {
				t.Errorf("write headers = %#v", request.Header)
			}
			if cookie, err := request.Cookie("session"); err != nil || cookie.Value != "ready" {
				t.Errorf("session cookie = %#v err=%v", cookie, err)
			}
			var body struct {
				Skills []string `json:"skills"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body.Skills) != 1 {
				t.Errorf("POST body = %#v err=%v", body, err)
			} else {
				current = body.Skills[0]
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"resume":{}}`))
		default:
			t.Errorf("unexpected method %s", request.Method)
		}
	}))
	proposal := browserProfileStateProposal(t, `"old"`, `"new"`)

	result, err := client.ApplyProfileState(context.Background(), proposal)
	if err != nil || result.AlreadyApplied || current != "new" || gets != 2 || posts != 1 {
		t.Fatalf("apply result=%#v current=%q gets=%d posts=%d err=%v", result, current, gets, posts, err)
	}
	result, err = client.ApplyProfileState(context.Background(), proposal)
	if err != nil || !result.AlreadyApplied || gets != 3 || posts != 1 {
		t.Fatalf("retry result=%#v gets=%d posts=%d err=%v", result, gets, posts, err)
	}
}

func TestBrowserProfileStateRejectsChangedPreconditionWithoutPOST(t *testing.T) {
	posts := 0
	client := newBrowserProfileStateClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			posts++
		}
		_, _ = response.Write([]byte(`<textarea data-qa="resume-editor-about">third</textarea>`))
	}))
	_, err := client.ApplyProfileState(context.Background(), browserProfileStateProposal(t, `"old"`, `"new"`))
	if !core.ErrorIsCategory(err, core.ErrorConflict) || posts != 0 {
		t.Fatalf("error=%v posts=%d, want conflict without POST", err, posts)
	}
}

func TestBrowserProfileStateReconcilesAmbiguousPOSTByReadBack(t *testing.T) {
	current := "old"
	client := newBrowserProfileStateClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			_, _ = response.Write([]byte(`<textarea data-qa="resume-editor-about">` + stdhtml.EscapeString(current) + `</textarea>`))
			return
		}
		var body struct {
			Skills []string `json:"skills"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		current = strings.Join(body.Skills, "")
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	result, err := client.ApplyProfileState(context.Background(), browserProfileStateProposal(t, `"old"`, `"new"`))
	if err != nil || result.AlreadyApplied || current != "new" {
		t.Fatalf("ambiguous reconciliation result=%#v current=%q err=%v", result, current, err)
	}
}

func TestBrowserProfileStateClearsAboutWithNull(t *testing.T) {
	current := "old"
	client := newBrowserProfileStateClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			_, _ = response.Write([]byte(`<textarea data-qa="resume-editor-about">` + stdhtml.EscapeString(current) + `</textarea>`))
			return
		}
		var body struct {
			Skills []string `json:"skills"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		if len(body.Skills) != 0 {
			t.Errorf("clear body = %#v", body)
		}
		current = ""
		_, _ = response.Write([]byte(`{"resume":{}}`))
	}))
	if _, err := client.ApplyProfileState(context.Background(), browserProfileStateProposal(t, `"old"`, `null`)); err != nil {
		t.Fatalf("clear about: %v", err)
	}
}
