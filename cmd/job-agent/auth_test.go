package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestAuthImportSanitizesStorageState(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "export.json")
	state := `{"cookies":[
		{"name":"hhtoken","domain":".hh.ru","value":"secret"},
		{"name":"foreign","domain":".example.com","value":"drop"}
	],"origins":[{"origin":"https://hh.ru","localStorage":[]},{"origin":"https://example.com","localStorage":[]}]}`
	if err := os.WriteFile(source, []byte(state), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	destination := filepath.Join(directory, "profiles", "primary.json")
	var output bytes.Buffer
	if err := runAuthImport([]string{"--source", source, "--state-output", destination}, &output); err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(output.String(), "IMPORTED cookies=1 origins=1") {
		t.Fatalf("output = %q", output.String())
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	var sanitized struct {
		Cookies []struct {
			Domain string `json:"domain"`
		} `json:"cookies"`
		Origins []struct {
			Origin string `json:"origin"`
		} `json:"origins"`
	}
	if err := json.Unmarshal(data, &sanitized); err != nil {
		t.Fatalf("decode destination: %v", err)
	}
	if len(sanitized.Cookies) != 1 || sanitized.Cookies[0].Domain != ".hh.ru" || len(sanitized.Origins) != 1 {
		t.Fatalf("sanitized state = %#v", sanitized)
	}
	if info, err := os.Stat(destination); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("destination permissions: %v %v", info, err)
	}
	if err := runAuthImport([]string{"--source", source, "--state-output", destination}, &output); err == nil ||
		!strings.Contains(err.Error(), "--force") {
		t.Fatalf("overwrite error = %v", err)
	}
}

func TestAuthStatusWatchFollowsSSE(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/auth/sessions/{id}/events", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := response.(http.Flusher)
		if !ok {
			t.Error("flusher is not supported")
			return
		}
		fmt.Fprintf(response, "event: session\ndata: {\"id\":\"auth-1\",\"profile_id\":\"primary\",\"status\":\"waiting_otp\",\"revision\":1}\n\n")
		flusher.Flush()
		fmt.Fprintf(response, "event: session\ndata: {\"id\":\"auth-1\",\"profile_id\":\"primary\",\"status\":\"completed\",\"revision\":2}\n\n")
		flusher.Flush()
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	var output bytes.Buffer
	err := runAuthStatus(context.Background(), []string{"--api", server.URL, "--session", "auth-1", "--watch"}, &output, server.Client())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	rendered := output.String()
	if !strings.Contains(rendered, "status=waiting_otp") || !strings.Contains(rendered, "status=completed") {
		t.Fatalf("output = %q", rendered)
	}
}

func TestAuthLogoutReportsRemovedSecrets(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/profiles/{profile}/logout", func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]bool{"credential_removed": true, "browser_state_removed": true})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	var output bytes.Buffer
	if err := runAuthLogout(context.Background(), []string{"--api", server.URL, "--profile", "primary"}, &output, server.Client()); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(output.String(), "credential_removed=true browser_state_removed=true") {
		t.Fatalf("output = %q", output.String())
	}
}
