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
