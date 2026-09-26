package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestResponseCacheServesRepeatReadsAndRekeysMutations(t *testing.T) {
	var calls int64
	product := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		atomic.AddInt64(&calls, 1)
		writeJSON(response, http.StatusOK, map[string]string{"value": "payload"})
	})
	handler := NewResponseCache().Middleware(product)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/applications?limit=10", nil))
	if first.Code != http.StatusOK || atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("first status=%d calls=%d", first.Code, calls)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing etag")
	}

	cached := httptest.NewRequest(http.MethodGet, "/api/v1/applications?limit=10", nil)
	cached.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, cached)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
		t.Fatalf("conditional status=%d body=%q", second.Code, second.Body.String())
	}
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("cache miss on repeat: calls=%d", calls)
	}

	// A mutation marks the reads stale: the next request is answered instantly
	// and a background refresh rebuilds the entry.
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/applications/remove", nil))
	third := httptest.NewRecorder()
	handler.ServeHTTP(third, httptest.NewRequest(http.MethodGet, "/api/v1/applications?limit=10", nil))
	if third.Code != http.StatusOK {
		t.Fatalf("after mutation status=%d", third.Code)
	}
	waitFor(t, func() bool { return atomic.LoadInt64(&calls) == 3 })
	fourth := httptest.NewRequest(http.MethodGet, "/api/v1/applications?limit=10", nil)
	fourth.Header.Set("If-None-Match", third.Header().Get("ETag"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, fourth)
	if response.Code != http.StatusNotModified {
		t.Fatalf("refreshed entry status=%d", response.Code)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met in time")
}

func TestResponseCacheLeavesUnconfiguredPathsAlone(t *testing.T) {
	var calls int64
	product := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		atomic.AddInt64(&calls, 1)
		writeJSON(response, http.StatusOK, map[string]string{"value": "tasks"})
	})
	handler := NewResponseCache().Middleware(product)
	for index := 0; index < 2; index++ {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/tasks/failed", nil))
	}
	if atomic.LoadInt64(&calls) != 2 {
		t.Fatalf("unconfigured path was cached: calls=%d", calls)
	}
}

func TestResponseCacheExpiresEntries(t *testing.T) {
	var calls int64
	product := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		atomic.AddInt64(&calls, 1)
		writeJSON(response, http.StatusOK, map[string]string{"value": "payload"})
	})
	cache := NewResponseCache()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }
	handler := cache.Middleware(product)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/conversations", nil))
	now = now.Add(16 * time.Second)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/conversations", nil))
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("stale entry must be served instantly: calls=%d", calls)
	}
	waitFor(t, func() bool { return atomic.LoadInt64(&calls) == 2 })
}
