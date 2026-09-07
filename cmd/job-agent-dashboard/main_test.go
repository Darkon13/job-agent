package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardServesAssetsAndProxiesAPI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/dashboard/summary" {
			t.Fatalf("proxied path: %s", request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"stats":{"tasks":2}}`))
	}))
	defer upstream.Close()
	handler, err := newDashboardHandler(upstream.URL)
	if err != nil {
		t.Fatalf("new dashboard handler: %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Job Agent") || !strings.Contains(response.Body.String(), "Профиль и резюме") {
		t.Fatalf("index response: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing content security policy")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "base_manifest_digest") || !strings.Contains(response.Body.String(), "Одноразовое изменение") || !strings.Contains(response.Body.String(), "/reconcile") {
		t.Fatalf("dashboard script response: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/summary", nil))
	body, _ := io.ReadAll(response.Result().Body)
	if response.Code != http.StatusOK || string(body) != `{"stats":{"tasks":2}}` {
		t.Fatalf("proxy response: %d %s", response.Code, body)
	}
}

func TestDashboardRejectsUnsafeUpstream(t *testing.T) {
	for _, value := range []string{"", "file:///tmp/api", "http://user:secret@example.test", "http://example.test/api", "http://example.test?token=secret"} {
		if _, err := newDashboardHandler(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}
