package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestRunProfileBootstrapPlansAndOptionallyApplies(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "resume.json")
	manifest := `{"api_version":"job-agent/v1","kind":"ProfileBootstrap","metadata":{"name":"primary-resume"},"spec":{"profile_id":"primary","state":{"resumes":{"resume-1":{"about":"Backend"}}}}}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	applyCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/profile-state/bootstrap/plans":
			if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("plan request = %s headers=%v", request.Method, request.Header)
			}
			data, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(data), `"profile_id":"primary"`) {
				t.Fatalf("plan body = %s", data)
			}
			proposal := core.ProfileStateProposal{
				ID: "proposal-1", ResourceTag: "primary-resume", ProfileID: "primary",
				Status: core.ProfileStateProposalPlanned, ManifestDigest: "sha256:manifest",
				Changes:   []core.ProfileStateChange{{Path: "/resumes/resume-1/about", Operation: "set"}},
				CreatedAt: time.Now().UTC(),
			}
			_ = json.NewEncoder(response).Encode(proposal)
		case "/api/v1/profile-state/proposals/proposal-1/apply":
			applyCalls++
			_ = json.NewEncoder(response).Encode(core.Task{ID: "task-1", Status: core.TaskNew})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	var output strings.Builder
	if err := runProfileBootstrap(context.Background(), []string{"--api", server.URL, manifestPath}, &output, server.Client()); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if applyCalls != 0 || !strings.Contains(output.String(), "PLAN proposal=proposal-1") || !strings.Contains(output.String(), "SET /resumes/resume-1/about") {
		t.Fatalf("plan output = %q apply calls=%d", output.String(), applyCalls)
	}
	output.Reset()
	if err := runProfileBootstrap(context.Background(), []string{"--api", server.URL, "--apply", manifestPath}, &output, server.Client()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applyCalls != 1 || !strings.Contains(output.String(), "APPLY task=task-1 status=new") {
		t.Fatalf("apply output = %q apply calls=%d", output.String(), applyCalls)
	}
}

func TestRunProfileBootstrapRejectsInvalidManifestBeforeRequest(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "resume.json")
	if err := os.WriteFile(manifestPath, []byte(`{"kind":"ProfileBootstrap"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid manifest reached backend")
		return nil, nil
	})}
	if err := runProfileBootstrap(context.Background(), []string{manifestPath}, io.Discard, client); err == nil {
		t.Fatal("expected invalid manifest to fail")
	}
}

func TestRunProfileStartupAppliesByDefault(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "resume.json")
	manifest := `{"api_version":"job-agent/v1","kind":"ProfileBootstrap","metadata":{"name":"primary-resume"},"spec":{"profile_id":"primary","state":{"resumes":{"resume-1":{"web":{"keySkills":["Go"]}}}}}}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	applies := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/profile-state/bootstrap/plans":
			_ = json.NewEncoder(response).Encode(core.ProfileStateProposal{ID: "proposal-1", Status: core.ProfileStateProposalPlanned})
		case "/api/v1/profile-state/proposals/proposal-1/apply":
			applies++
			_ = json.NewEncoder(response).Encode(core.Task{ID: "task-1", Status: core.TaskNew})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	if err := runProfileStartup(context.Background(), []string{"--api", server.URL, manifestPath}, io.Discard, server.Client()); err != nil {
		t.Fatalf("startup: %v", err)
	}
	if applies != 1 {
		t.Fatalf("apply calls = %d, want 1", applies)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
