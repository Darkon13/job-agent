package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunProfileListAndShow(t *testing.T) {
	modified := time.Date(2026, 9, 27, 11, 20, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/profiles" {
			http.NotFound(response, request)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"items": []map[string]any{
			{
				"tag": "primary", "adapter": "hh-main", "platform": "hh", "enabled": true, "source": "config",
				"identity": map[string]any{"display_name": "Антон", "email": "a***@mail.ru", "account_hash": "sha256:ab12"},
				"resumes": []map[string]any{
					{"id": "resume-9", "title": "Go developer", "primary": true},
					{"id": "resume-8"},
				},
				"session": map[string]any{"state_file": "/data/profiles/primary/state.json", "present": true, "modified_at": modified},
			},
			{
				"tag": "secondary", "adapter": "hh-main", "platform": "hh", "enabled": false, "source": "profile_store",
				"resumes": []any{}, "session": map[string]any{"present": false},
			},
		}})
	}))
	defer server.Close()

	var output strings.Builder
	if err := runProfile(context.Background(), []string{"list", "--api", server.URL}, &output, server.Client()); err != nil {
		t.Fatalf("list: %v", err)
	}
	text := output.String()
	for _, want := range []string{
		"primary (hh-main/hh, включён, источник: config)",
		"Антон", "a***@mail.ru", "аккаунт sha256:ab12",
		"Go developer (resume-9) — основное", "resume-8",
		"сессия: есть",
		"secondary (hh-main/hh, выключен, источник: profile_store)", "резюме: нет", "сессия: нет",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("list output misses %q:\n%s", want, text)
		}
	}
	output.Reset()
	if err := runProfile(context.Background(), []string{"show", "secondary", "--api", server.URL}, &output, server.Client()); err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(output.String(), "primary") || !strings.Contains(output.String(), "secondary (hh-main/hh, выключен") {
		t.Fatalf("show output = %q", output.String())
	}
	output.Reset()
	if err := runProfile(context.Background(), []string{"show", "missing", "--api", server.URL}, &output, server.Client()); err == nil {
		t.Fatal("expected an unknown profile to fail")
	}
	output.Reset()
	if err := runProfile(context.Background(), []string{"unknown", "--api", server.URL}, &output, server.Client()); err == nil {
		t.Fatal("expected an unknown subcommand to fail")
	}
}

func TestRunProfileAddDrivesDraftOnboarding(t *testing.T) {
	var created map[string]string
	applied := ""
	restart := false
	inputs := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/version", func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]string{"api_version": "v1"})
	})
	mux.HandleFunc("POST /api/v1/profile-drafts", func(response http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&created); err != nil {
			t.Errorf("decode draft request: %v", err)
		}
		response.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"tag": "secondary", "platform": "hh", "adapter": "hh-main",
			"state_file": "/store/secondary/state.json", "status": "pending",
		})
	})
	mux.HandleFunc("POST /api/v1/auth/sessions", func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "auth-1", "platform": "hh", "profile_id": "secondary", "status": "waiting_identifier",
		})
	})
	mux.HandleFunc("POST /api/v1/auth/sessions/{id}/inputs", func(response http.ResponseWriter, _ *http.Request) {
		inputs++
		status := "waiting_otp"
		if inputs > 1 {
			status = "completed"
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "auth-1", "platform": "hh", "profile_id": "secondary", "status": status,
		})
	})
	mux.HandleFunc("POST /api/v1/profile-drafts/secondary/refresh", func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{
			"tag": "secondary", "platform": "hh", "adapter": "hh-main",
			"state_file": "/store/secondary/state.json", "status": "ready",
			"identity": map[string]any{"display_name": "Антон Шумаков", "email": "u***@example.test"},
			"resumes": []map[string]any{
				{"id": "resume-9", "title": "Go developer"},
				{"id": "resume-8"},
			},
		})
	})
	mux.HandleFunc("POST /api/v1/profile-drafts/secondary/apply", func(response http.ResponseWriter, request *http.Request) {
		var body struct {
			Primary string `json:"primary_resume"`
			Restart bool   `json:"restart"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode apply request: %v", err)
		}
		applied = body.Primary
		restart = body.Restart
		_ = json.NewEncoder(response).Encode(map[string]any{
			"tag": "secondary", "platform": "hh", "adapter": "hh-main",
			"state_file": "/store/secondary/state.json", "status": "applied",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	prompts := []string{"user@example.test", "1234", "2"}
	prompt := func(string) (string, error) {
		value := prompts[0]
		prompts = prompts[1:]
		return value, nil
	}
	var output strings.Builder
	if err := runProfileAddWith(context.Background(), []string{"secondary", "--api", server.URL, "--restart"}, &output, server.Client(), prompt); err != nil {
		t.Fatalf("profile add: %v", err)
	}
	if created["tag"] != "secondary" {
		t.Fatalf("draft request = %#v", created)
	}
	if applied != "resume-8" || !restart {
		t.Fatalf("applied primary = %q restart=%t", applied, restart)
	}
	for _, want := range []string{"DRAFT tag=secondary", "IDENTITY Антон Шумаков", "RESUME Go developer (resume-9)", "APPLIED tag=secondary status=applied", "backend перезапускается"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output misses %q:\n%s", want, output.String())
		}
	}
}
