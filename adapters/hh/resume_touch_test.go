package hh

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

func touchTransportFixture(t *testing.T, canTouch bool, nextTouchAt int64, touchStatus int) (*ResumeTouchTransport, *int) {
	t.Helper()
	touches := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/profile":
			response.Header().Set("Content-Type", "text/html")
			state := map[string]any{"applicantResumes": []any{map[string]any{"_attributes": map[string]any{
				"id": "42", "hash": "resume-hash", "canTouch": canTouch, "nextTouchAt": nextTouchAt, "update_timeout": 14_400_000,
			}}}}
			encoded, _ := json.Marshal(state)
			_, _ = response.Write([]byte(`<template class="ResumeProfileFront-InitialState">` + string(encoded) + `</template>`))
		case "/touch":
			*touches++
			if request.Header.Get("X-Xsrftoken") != "test-xsrf" || request.Header.Get("X-Requested-With") != "XMLHttpRequest" {
				t.Errorf("missing touch headers: %#v", request.Header)
			}
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["hash"] != "resume-hash" {
				t.Errorf("unexpected touch body: %#v err=%v", body, err)
			}
			response.WriteHeader(touchStatus)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := browserStorageState{Cookies: []browserCookie{{Name: "_xsrf", Value: "test-xsrf", Path: "/"}}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	transport, err := NewResumeTouchTransport(statePath, server.Client())
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	transport.profileURL = server.URL + "/profile"
	transport.touchURL = server.URL + "/touch"
	return transport, touches
}

func TestResumeTouchTransportUsesNextTouchAtWithoutEarlyPOST(t *testing.T) {
	next := time.Date(2026, 7, 19, 16, 2, 47, 683_000_000, time.UTC)
	transport, touches := touchTransportFixture(t, false, next.UnixMilli(), http.StatusNoContent)
	result, err := transport.TouchResume(context.Background(), adapter.ResumeTouchCommand{ProfileID: "primary", ResumeID: "resume-hash"})
	var operationError *core.OperationError
	if !errors.As(err, &operationError) || operationError.Category != core.ErrorRateLimited || operationError.RetryAfter == nil || !operationError.RetryAfter.Equal(next) {
		t.Fatalf("unexpected early touch result=%#v err=%v", result, err)
	}
	if *touches != 0 {
		t.Fatalf("early touch performed %d POST requests", *touches)
	}
}

func TestResumeTouchTransportPostsHashWithXSRF(t *testing.T) {
	transport, touches := touchTransportFixture(t, true, 0, http.StatusNoContent)
	if _, err := transport.TouchResume(context.Background(), adapter.ResumeTouchCommand{ProfileID: "primary", ResumeID: "42"}); err != nil {
		t.Fatalf("touch resume: %v", err)
	}
	if *touches != 1 {
		t.Fatalf("touch requests = %d, want 1", *touches)
	}
}
