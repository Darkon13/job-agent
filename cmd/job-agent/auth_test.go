package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthLoginDrivesInteractiveSession(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "primary.state.json")

	var created authSessionRequest
	var inputs []authInputRequest
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/version", func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]string{"api_version": "v1"})
	})
	mux.HandleFunc("POST /api/v1/auth/sessions", func(response http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&created); err != nil {
			t.Errorf("decode create request: %v", err)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "auth-1", "platform": "hh", "profile_id": "primary", "status": "waiting_identifier",
		})
	})
	mux.HandleFunc("POST /api/v1/auth/sessions/{id}/inputs", func(response http.ResponseWriter, request *http.Request) {
		var input authInputRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode input: %v", err)
		}
		inputs = append(inputs, input)
		status := "waiting_otp"
		if len(inputs) > 1 {
			status = "completed"
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "auth-1", "platform": "hh", "profile_id": "primary", "status": status,
			"browser_state_reference": statePath,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	prompts := []string{"user@example.com", "1234"}
	prompt := func(string) (string, error) {
		value := prompts[0]
		prompts = prompts[1:]
		return value, nil
	}
	var output bytes.Buffer
	err := runAuthLoginWith(context.Background(), []string{
		"--api", server.URL, "--profile", "primary", "--state-output", statePath,
	}, &output, server.Client(), prompt)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if created.Platform != "hh" || created.ProfileID != "primary" || created.BrowserStateReference != statePath {
		t.Fatalf("create request = %#v", created)
	}
	if len(inputs) != 2 || inputs[0].Kind != "identifier" || inputs[0].Value != "user@example.com" ||
		inputs[1].Kind != "otp" || inputs[1].Value != "1234" {
		t.Fatalf("inputs = %#v", inputs)
	}
	if !strings.Contains(output.String(), "DONE status=completed") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestAuthLoginRequiresForceToReplaceCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hh-primary.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"old"}`), 0o600); err != nil {
		t.Fatalf("write credential: %v", err)
	}
	err := runAuthLoginWith(context.Background(), []string{
		"--api", "http://127.0.0.1:1", "--profile", "primary", "--credential-output", path,
	}, &bytes.Buffer{}, nil, func(string) (string, error) { return "", nil })
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error = %v, want force hint", err)
	}
}

func TestAuthLoginRejectsCredentialStdout(t *testing.T) {
	err := runAuthLoginWith(context.Background(), []string{
		"--api", "http://127.0.0.1:1", "--profile", "primary", "--credential-output", "-",
	}, &bytes.Buffer{}, nil, func(string) (string, error) { return "", nil })
	if err == nil || !strings.Contains(err.Error(), "OAuth token exchange") {
		t.Fatalf("error = %v", err)
	}
}
