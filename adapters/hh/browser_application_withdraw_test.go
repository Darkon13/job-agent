package hh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/Darkon13/job-agent/core"
)

func newWithdrawFixture(t *testing.T, handler http.Handler) *BrowserReadClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state := browserStorageState{Cookies: []browserCookie{
		{Name: "session", Value: "ready", Path: "/"},
		{Name: "_xsrf", Value: "xsrf-value", Path: "/"},
	}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode browser state: %v", err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatalf("write browser state: %v", err)
	}
	client, err := NewBrowserReadClient("primary", statePath, "JobAgent/Test", server.Client())
	if err != nil {
		t.Fatalf("new browser read client: %v", err)
	}
	client.webBaseURL = server.URL
	return client
}

func TestBrowserWithdrawalDeclinesPendingAndTrashesClosedTopics(t *testing.T) {
	var requests []url.Values
	var paths []string
	client := newWithdrawFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %s", request.Method)
		}
		if request.Header.Get("X-Xsrftoken") == "" {
			t.Errorf("missing X-Xsrftoken on %s", request.URL.Path)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		paths = append(paths, request.URL.Path)
		requests = append(requests, request.PostForm)
		_, _ = response.Write([]byte(`{}`))
	}))

	pending, err := client.WithdrawApplication(context.Background(), "primary", core.ApplicationPlatformState{
		ExternalNegotiationID: "5566260191", Disposition: core.ApplicationDispositionPending,
	})
	if err != nil || pending.Action != "decline" {
		t.Fatalf("decline pending: result=%#v err=%v", pending, err)
	}
	rejected, err := client.WithdrawApplication(context.Background(), "primary", core.ApplicationPlatformState{
		ExternalNegotiationID: "5565658117", Disposition: core.ApplicationDispositionRejected,
	})
	if err != nil || rejected.Action != "trash" {
		t.Fatalf("trash rejected: result=%#v err=%v", rejected, err)
	}
	if len(paths) != 2 || paths[0] != "/applicant/negotiations/decline" || paths[1] != "/applicant/negotiations/trash" {
		t.Fatalf("paths = %#v", paths)
	}
	if requests[0].Get("topic") != "5566260191" || requests[0].Get("substate") != "" {
		t.Fatalf("decline form = %#v", requests[0])
	}
	if requests[1].Get("topic") != "5565658117" || requests[1].Get("substate") != "HIDE" {
		t.Fatalf("trash form = %#v", requests[1])
	}
}

func TestBrowserWithdrawalRejectsUnappliedAction(t *testing.T) {
	client := newWithdrawFixture(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<?xml version='1.0' encoding='utf-8'?><doc/>`))
	}))
	_, err := client.WithdrawApplication(context.Background(), "primary", core.ApplicationPlatformState{
		ExternalNegotiationID: "5565658117", Disposition: core.ApplicationDispositionRejected,
	})
	if err == nil {
		t.Fatal("a generic <doc/> answer must not count as an applied withdrawal")
	}
	if !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("unapplied action category = %v", err)
	}
}

func TestBrowserWithdrawalRequiresNegotiationIdentity(t *testing.T) {
	client := newWithdrawFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("unexpected request")
	}))
	if _, err := client.WithdrawApplication(context.Background(), "primary", core.ApplicationPlatformState{}); err == nil {
		t.Fatal("expected missing negotiation identity to fail")
	}
}
