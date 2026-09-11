package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQualificationCatalogRendersOfferings(t *testing.T) {
	score, maxScore := 9.0, 10.0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/profiles/{profile}/qualifications", func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id": "hh:offer-1", "platform": "hh", "profile_id": "primary", "external_id": "go-medium",
					"qualification": map[string]string{"family_id": "go", "family_name": "Go", "level_id": "medium", "level_name": "Средний"},
					"status":        "available",
					"best_result":   map[string]any{"status": "passed", "score": score, "max_score": maxScore, "verified": true},
					"observed_at":   "2026-09-11T12:00:00Z",
				},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	var output bytes.Buffer
	if err := runQualificationCatalog(context.Background(), []string{"--api", server.URL, "--profile", "primary"}, &output, server.Client()); err != nil {
		t.Fatalf("catalog: %v", err)
	}
	rendered := output.String()
	for _, fragment := range []string{"offering=hh:offer-1", `family="Go"`, `level="Средний"`, "status=available", "best=passed 9/10"} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("output %q misses %q", rendered, fragment)
		}
	}
}

func TestQualificationStartPostsIdempotently(t *testing.T) {
	var receivedPath string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/profiles/{profile}/qualifications/{offering}/start", func(response http.ResponseWriter, request *http.Request) {
		receivedPath = request.URL.Path
		if request.Header.Get("Idempotency-Key") == "" {
			t.Error("missing idempotency key")
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"task_id": "task-1", "created": true})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	var output bytes.Buffer
	err := runQualificationStart(context.Background(), []string{
		"--api", server.URL, "--profile", "primary", "--offering", "hh:offer-1",
	}, &output, server.Client())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if receivedPath != "/api/v1/profiles/primary/qualifications/hh:offer-1/start" {
		t.Fatalf("path = %q", receivedPath)
	}
	if !strings.Contains(output.String(), "task=task-1 created=true") {
		t.Fatalf("output = %q", output.String())
	}
}
