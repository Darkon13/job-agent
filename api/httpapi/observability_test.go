package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Darkon13/job-agent/storage"
)

func TestRequestIDPreservesAndGeneratesIDs(t *testing.T) {
	var fromContext string
	handler := RequestID(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		fromContext = RequestIDFromContext(request.Context())
		response.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	request.Header.Set("X-Request-ID", "given-id")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("X-Request-ID") != "given-id" || fromContext != "given-id" {
		t.Fatalf("preserved id: header=%q context=%q", response.Header().Get("X-Request-ID"), fromContext)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	generated := response.Header().Get("X-Request-ID")
	if len(generated) != 24 || fromContext != generated {
		t.Fatalf("generated id: header=%q context=%q", generated, fromContext)
	}
}

func TestAccessLogWritesStructuredLine(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))
	handler := RequestID(AccessLog(logger, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "boom", http.StatusServiceUnavailable)
	})))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/review-sessions/review-1/answers", nil)
	request.Header.Set("X-Request-ID", "trace-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	line := strings.TrimSpace(buffer.String())
	if line == "" {
		t.Fatal("missing access log line")
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("decode access log: %v line=%s", err, line)
	}
	if entry["msg"] != "http_request" || entry["method"] != "POST" || entry["status"] != float64(503) || entry["request_id"] != "trace-1" {
		t.Fatalf("entry = %#v", entry)
	}
	if _, exists := entry["duration_ms"]; !exists {
		t.Fatalf("missing duration: %#v", entry)
	}
}

func TestMetricsAPICountsStatusClasses(t *testing.T) {
	api := NewMetricsAPI(nil)
	handler := api.Handler(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ok":
			response.WriteHeader(http.StatusOK)
		case "/missing":
			http.NotFound(response, request)
		default:
			response.WriteHeader(http.StatusInternalServerError)
		}
	}))
	for _, path := range []string{"/ok", "/missing", "/boom"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	body := httptest.NewRecorder()
	handler.ServeHTTP(body, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if body.Code != http.StatusOK || !strings.Contains(body.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics response: %d %q", body.Code, body.Header().Get("Content-Type"))
	}
	rendered := body.Body.String()
	for _, fragment := range []string{
		`job_agent_http_requests_total{class="2xx"} 1`,
		`job_agent_http_requests_total{class="4xx"} 1`,
		`job_agent_http_requests_total{class="5xx"} 1`,
		"job_agent_http_in_flight 1",
		"job_agent_uptime_seconds",
	} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("metrics miss %q:\n%s", fragment, rendered)
		}
	}
}

type stubTaskCounts struct {
	counts []storage.TaskCount
	err    error
}

func (stub stubTaskCounts) TaskCounts(context.Context) ([]storage.TaskCount, error) {
	return stub.counts, stub.err
}

func TestMetricsAPIExposesTaskQueue(t *testing.T) {
	api := NewMetricsAPI(stubTaskCounts{counts: []storage.TaskCount{
		{Type: "application.submit", Status: "new", Priority: 0, Count: 2},
	}})
	handler := api.Handler(http.NotFoundHandler())
	body := httptest.NewRecorder()
	handler.ServeHTTP(body, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(body.Body.String(), `job_agent_tasks{type="application.submit",status="new",priority="0"} 2`) {
		t.Fatalf("metrics = %s", body.Body.String())
	}
	failing := NewMetricsAPI(stubTaskCounts{err: errors.New("storage down")})
	body = httptest.NewRecorder()
	failing.Handler(http.NotFoundHandler()).ServeHTTP(body, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(body.Body.String(), "job_agent_task_counts_error 1") {
		t.Fatalf("metrics = %s", body.Body.String())
	}
}
