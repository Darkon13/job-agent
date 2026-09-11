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

func TestReviewShowRendersWaitingPrompt(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/review-sessions/{id}", func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "review-1", "profile_id": "primary", "status": "waiting_answer", "revision": 1,
			"prompt": map[string]any{
				"id": "review-1-prompt-1", "revision": 1,
				"question": map[string]any{
					"id": "2", "text": "Rate yourself", "kind": "single",
					"options": []map[string]string{{"id": "20", "text": "Junior"}, {"id": "21", "text": "Senior"}},
				},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	var output bytes.Buffer
	if err := runReviewShow(context.Background(), []string{"--api", server.URL, "--session", "review-1"}, &output, server.Client()); err != nil {
		t.Fatalf("show: %v", err)
	}
	rendered := output.String()
	for _, fragment := range []string{"session=review-1", "status=waiting_answer", `question="Rate yourself"`, `option="Senior"`} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("output %q misses %q", rendered, fragment)
		}
	}
}

func TestReviewAnswerPostsIdempotentSelection(t *testing.T) {
	var received map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/review-sessions/{id}/answers", func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Idempotency-Key") == "" {
			t.Error("missing idempotency key")
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"task_id": "task-1", "created": true})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	var output bytes.Buffer
	err := runReviewAnswer(context.Background(), []string{
		"--api", server.URL, "--session", "review-1", "--prompt", "review-1-prompt-1",
		"--revision", "1", "--option", "Senior",
	}, &output, server.Client())
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if !strings.Contains(output.String(), "task=task-1 created=true") {
		t.Fatalf("output = %q", output.String())
	}
	if received["prompt_id"] != "review-1-prompt-1" || received["source"] != "cli" {
		t.Fatalf("received = %#v", received)
	}
	options, ok := received["selected_options"].([]any)
	if !ok || len(options) != 1 || options[0] != "Senior" {
		t.Fatalf("options = %#v", received["selected_options"])
	}
}
