package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func newTestClient(t *testing.T, handler http.Handler) (*HTTPClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewHTTPClient(HTTPConfig{BaseURL: server.URL, Token: "worker-secret"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, server
}

func TestEnsureSendsAuthAndParsesContext(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/profiles/primary/ensure" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer worker-secret" || request.Header.Get("X-Browser-Protocol") != ProtocolVersion {
			t.Errorf("missing worker headers: %v", request.Header)
		}
		var body EnsureRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Headless == nil || !*body.Headless {
			t.Errorf("unexpected body: %#v err=%v", body, err)
		}
		response.Header().Set("X-Browser-Protocol", ProtocolVersion)
		_, _ = response.Write([]byte(`{"profile_id":"primary","created":true,"pages":1,"headless":true}`))
	}))
	headless := true
	info, err := client.Ensure(context.Background(), "primary", EnsureRequest{Headless: &headless})
	if err != nil || info.ProfileID != "primary" || !info.Created || !info.Headless {
		t.Fatalf("info=%#v err=%v", info, err)
	}
}

func TestScreenshotReturnsImageBytes(t *testing.T) {
	data := []byte{0x89, 'P', 'N', 'G'}
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/profiles/primary/screenshot" || request.Header.Get("Accept") != "image/png" {
			t.Errorf("unexpected screenshot request: %s accept=%q", request.URL.Path, request.Header.Get("Accept"))
		}
		response.Header().Set("Content-Type", "image/png")
		response.Header().Set("X-Browser-Protocol", ProtocolVersion)
		_, _ = response.Write(data)
	}))
	screenshot, err := client.Screenshot(context.Background(), "primary", ScreenshotRequest{Selector: ".captcha"})
	if err != nil || string(screenshot) != string(data) {
		t.Fatalf("screenshot=%v err=%v", screenshot, err)
	}
}

func TestWorkerErrorsMapToCoreCategories(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		code     string
		category core.ErrorCategory
	}{
		{name: "invalid", status: http.StatusBadRequest, code: "invalid", category: core.ErrorPermanentFailure},
		{name: "unauthorized", status: http.StatusUnauthorized, code: "unauthorized", category: core.ErrorUnauthorized},
		{name: "not found", status: http.StatusNotFound, code: "not_found", category: core.ErrorPermanentFailure},
		{name: "busy", status: http.StatusConflict, code: "busy", category: core.ErrorConflict},
		{name: "timeout", status: http.StatusGatewayTimeout, code: "timeout", category: core.ErrorTemporaryFailure},
		{name: "unsupported", status: http.StatusBadRequest, code: "unsupported", category: core.ErrorUnsupported},
		{name: "internal", status: http.StatusInternalServerError, code: "internal", category: core.ErrorTemporaryFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("X-Browser-Protocol", ProtocolVersion)
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(`{"error":{"code":"` + test.code + `","message":"worker failure"}}`))
			}))
			_, err := client.Goto(context.Background(), "primary", GotoRequest{URL: "https://hh.ru"})
			var operationError *core.OperationError
			if !errors.As(err, &operationError) || operationError.Category != test.category {
				t.Fatalf("error=%#v category=%v", err, test.category)
			}
		})
	}
}

func TestProtocolMismatchAndTransportFailureAreNormalized(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-Browser-Protocol", "2")
		_, _ = response.Write([]byte(`{}`))
	}))
	_, err := client.Goto(context.Background(), "primary", GotoRequest{URL: "https://hh.ru"})
	var operationError *core.OperationError
	if !errors.As(err, &operationError) || operationError.Category != core.ErrorPermanentFailure {
		t.Fatalf("protocol error = %#v", err)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	offline, err := NewHTTPClient(HTTPConfig{BaseURL: closed.URL, Token: "secret", Client: &http.Client{Timeout: time.Second}})
	if err != nil {
		t.Fatalf("new offline client: %v", err)
	}
	_, err = offline.Health(context.Background())
	if !errors.As(err, &operationError) || operationError.Category != core.ErrorTemporaryFailure {
		t.Fatalf("transport error = %#v", err)
	}
}

func TestProfileOperationsAreSerializedPerProfile(t *testing.T) {
	var primaryInFlight, secondaryInFlight int32
	var maximumPrimary, maximumSecondary int32
	var overlap int32
	release := make(chan struct{})
	var wg sync.WaitGroup

	observe := func(current int32, maximum *int32) {
		for {
			previous := atomic.LoadInt32(maximum)
			if current <= previous || atomic.CompareAndSwapInt32(maximum, previous, current) {
				return
			}
		}
	}

	client, _ := newTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/profiles/primary/goto":
			current := atomic.AddInt32(&primaryInFlight, 1)
			observe(current, &maximumPrimary)
			if atomic.LoadInt32(&secondaryInFlight) > 0 {
				atomic.StoreInt32(&overlap, 1)
			}
			<-release
			atomic.AddInt32(&primaryInFlight, -1)
		default:
			current := atomic.AddInt32(&secondaryInFlight, 1)
			observe(current, &maximumSecondary)
			<-release
			atomic.AddInt32(&secondaryInFlight, -1)
		}
		response.Header().Set("X-Browser-Protocol", ProtocolVersion)
		_, _ = response.Write([]byte(`{"url":"https://hh.ru","status":200,"title":"HH"}`))
	}))

	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = client.Goto(context.Background(), "primary", GotoRequest{URL: "https://hh.ru"})
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = client.Goto(context.Background(), "secondary", GotoRequest{URL: "https://hh.ru"})
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if maximumPrimary != 1 || maximumSecondary != 1 {
		t.Fatalf("per-profile concurrency primary=%d secondary=%d, want 1", maximumPrimary, maximumSecondary)
	}
	if atomic.LoadInt32(&overlap) != 1 {
		t.Fatal("different profiles did not overlap")
	}
}
