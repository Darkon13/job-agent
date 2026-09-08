package hh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

func newAPIProfileStateClient(t *testing.T, handler http.Handler) (*ReadClient, func() int) {
	t.Helper()
	credentialsPath := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(credentialsPath, []byte(`{"access_token":"secret-token"}`), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("Authorization") != "Bearer secret-token" || request.Header.Get("HH-User-Agent") == "" {
			t.Fatalf("authorization headers = %v", request.Header)
		}
		handler.ServeHTTP(response, request)
	}))
	t.Cleanup(server.Close)
	client, err := NewReadClient("primary", credentialsPath, "JobAgent/test", server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client.apiBaseURL = server.URL
	return client, func() int { return requests }
}

func TestAPIProfileStateReadsOnlyDeclaredNativeSections(t *testing.T) {
	client, _ := newAPIProfileStateClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/resume_profile/resume-42" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		_, _ = response.Write([]byte(`{
			"current_screen_id":"position",
			"profile":{"first_name":"Ivan","area":{"id":"1","name":"Moscow"},"private":"keep"},
			"resume":{"title":"Old","skill_set":["Go"],"experience":[],"private":"keep"},
			"creds":{},"additional_properties":{}
		}`))
	}))
	observation, err := client.ReadProfileState(context.Background(), adapter.ProfileStateReadRequest{
		ProfileID: "primary",
		Paths: []string{
			"/resumes/resume-42/profile/area/id",
			"/resumes/resume-42/resume/experience",
			"/resumes/resume-42/resume/skill_set",
		},
	})
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	want := `{"resumes":{"resume-42":{"profile":{"area":{"id":"1"}},"resume":{"experience":[],"skill_set":["Go"]}}}}`
	if string(observation.State) != want {
		t.Fatalf("state = %s, want %s", observation.State, want)
	}
}

func TestAPIProfileStateMergesDeclaredFieldsAndPreservesFreshSchema(t *testing.T) {
	remote := map[string]any{
		"current_screen_id": "position",
		"profile": map[string]any{
			"first_name": "Old", "area": map[string]any{"id": "1", "name": "Moscow"}, "private": "keep-profile",
		},
		"resume": map[string]any{
			"title": "Old title", "skill_set": []any{"Go"}, "experience": []any{}, "private": "keep-resume",
		},
		"creds":                 map[string]any{"private": "keep-creds"},
		"additional_properties": map[string]any{"private": "keep-additional"},
	}
	putCalls := 0
	client, requestCount := newAPIProfileStateClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			_ = json.NewEncoder(response).Encode(remote)
		case http.MethodPut:
			putCalls++
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode update: %v", err)
			}
			if body["current_screen_id"] != "position" {
				t.Fatalf("current_screen_id = %#v", body["current_screen_id"])
			}
			profile := body["profile"].(map[string]any)
			resume := body["resume"].(map[string]any)
			if profile["private"] != "keep-profile" || resume["private"] != "keep-resume" || body["creds"].(map[string]any)["private"] != "keep-creds" || body["additional_properties"].(map[string]any)["private"] != "keep-additional" {
				t.Fatalf("update dropped fresh schema fields: %#v", body)
			}
			if !reflect.DeepEqual(resume["skill_set"], []any{"Go", "PostgreSQL"}) || !reflect.DeepEqual(resume["experience"], []any{map[string]any{"company": "Example", "position": "Developer"}}) {
				t.Fatalf("atomic arrays = %#v", resume)
			}
			remote = body
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("request method = %s", request.Method)
		}
	}))
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	desired := json.RawMessage(`{"resumes":{"resume-42":{
		"profile":{"first_name":"Ivan"},
		"resume":{"skill_set":["Go","PostgreSQL"],"experience":[{"company":"Example","position":"Developer"}]}
	}}}`)
	resource, err := core.NewProfileStateResource("primary-resume", "primary", core.ProfileStateOwnershipDeclaredFields, desired)
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	before, err := client.ReadProfileState(context.Background(), adapter.ProfileStateReadRequest{ProfileID: "primary", Paths: mustProfileStatePaths(t, resource)})
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	proposal, err := core.NewProfileStateProposal("proposal-1", resource, before, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	result, err := client.ApplyProfileState(context.Background(), proposal)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.AlreadyApplied || putCalls != 1 || requestCount() != 4 {
		t.Fatalf("result = %#v puts=%d requests=%d", result, putCalls, requestCount())
	}
	remaining, err := proposal.ChangesToApply(result.Observation)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining = %#v err=%v", remaining, err)
	}

	result, err = client.ApplyProfileState(context.Background(), proposal)
	if err != nil || !result.AlreadyApplied || putCalls != 1 {
		t.Fatalf("idempotent apply = %#v puts=%d err=%v", result, putCalls, err)
	}
}

func TestAPIProfileStateRejectsNonNativePathBeforeRequest(t *testing.T) {
	client, requestCount := newAPIProfileStateClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unsupported path reached HH")
	}))
	_, err := client.ReadProfileState(context.Background(), adapter.ProfileStateReadRequest{
		ProfileID: "primary", Paths: []string{"/resumes/resume-42/about"},
	})
	if !core.ErrorIsCategory(err, core.ErrorUnsupported) || requestCount() != 0 {
		t.Fatalf("err=%v requests=%d", err, requestCount())
	}
}

func TestMergeResumeProfileUpdateDoesNotInventMissingOptionalSections(t *testing.T) {
	body, err := mergeResumeProfileUpdate(
		map[string]any{"resume": map[string]any{"title": "Old"}},
		map[string]any{"resume": map[string]any{"title": "New"}},
	)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(body) != 1 || body["profile"] != nil || body["creds"] != nil || body["additional_properties"] != nil {
		t.Fatalf("merge invented missing sections: %#v", body)
	}
	if body["resume"].(map[string]any)["title"] != "New" {
		t.Fatalf("resume = %#v", body["resume"])
	}
}

func mustProfileStatePaths(t *testing.T, resource core.ProfileStateResource) []string {
	t.Helper()
	paths, err := resource.DeclaredPaths()
	if err != nil {
		t.Fatalf("declared paths: %v", err)
	}
	return paths
}
