package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ResponseCache serves repeatable read endpoints from memory for a short
// window. Mutations clear the whole cache, so an operator action is never
// hidden behind stale data for longer than the TTL of the next read.
type ResponseCache struct {
	mu      sync.Mutex
	entries map[string]cachedResponse
	rules   []cacheRule
	now     func() time.Time
}

type cacheRule struct {
	path string
	ttl  time.Duration
}

type cachedResponse struct {
	status      int
	contentType string
	body        []byte
	etag        string
	expires     time.Time
}

// maximumCacheEntries bounds one process cache; the dashboard uses a handful
// of filter combinations, so a blunt reset is enough when it grows.
const maximumCacheEntries = 256

// NewResponseCache configures the per-endpoint TTLs. States written by workers
// (tasks, decisions, limits) stay uncached.
func NewResponseCache() *ResponseCache {
	return &ResponseCache{
		entries: make(map[string]cachedResponse),
		now:     time.Now,
		rules: []cacheRule{
			{path: "/api/v1/dashboard/summary", ttl: 10 * time.Second},
			{path: "/api/v1/applications", ttl: 10 * time.Second},
			{path: "/api/v1/conversations", ttl: 15 * time.Second},
			{path: "/api/v1/review-sessions", ttl: 15 * time.Second},
			{path: "/api/v1/jobs", ttl: 30 * time.Second},
		},
	}
}

func (cache *ResponseCache) ttlFor(path string) (time.Duration, bool) {
	for _, rule := range cache.rules {
		if path == rule.path || strings.HasPrefix(path, rule.path+"/") {
			return rule.ttl, true
		}
	}
	return 0, false
}

// Middleware caches successful GET responses of the configured endpoints and
// answers If-None-Match with 304.
func (cache *ResponseCache) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			cache.invalidate()
			next.ServeHTTP(response, request)
			return
		}
		// Event streams and anything not configured pass through untouched.
		if strings.Contains(request.URL.Path, "/events") {
			next.ServeHTTP(response, request)
			return
		}
		ttl, ok := cache.ttlFor(request.URL.Path)
		if !ok {
			next.ServeHTTP(response, request)
			return
		}
		key := request.URL.Path + "?" + request.URL.RawQuery
		if entry, found := cache.lookup(key); found {
			cache.write(response, request, entry)
			return
		}
		buffer := newBufferingWriter()
		next.ServeHTTP(buffer, request)
		entry, stored := cache.store(key, ttl, buffer)
		if stored {
			cache.write(response, request, entry)
			return
		}
		buffer.flush(response)
	})
}

func (cache *ResponseCache) lookup(key string) (cachedResponse, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, exists := cache.entries[key]
	if !exists {
		return cachedResponse{}, false
	}
	if !cache.now().Before(entry.expires) {
		delete(cache.entries, key)
		return cachedResponse{}, false
	}
	return entry, true
}

func (cache *ResponseCache) store(key string, ttl time.Duration, buffer *bufferingWriter) (cachedResponse, bool) {
	if buffer.status != http.StatusOK || buffer.body.Len() == 0 {
		return cachedResponse{}, false
	}
	digest := sha256.Sum256(buffer.body.Bytes())
	entry := cachedResponse{
		status:      buffer.status,
		contentType: buffer.header.Get("Content-Type"),
		body:        append([]byte(nil), buffer.body.Bytes()...),
		etag:        `"` + hex.EncodeToString(digest[:8]) + `"`,
		expires:     cache.now().Add(ttl),
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) >= maximumCacheEntries {
		cache.entries = make(map[string]cachedResponse)
	}
	cache.entries[key] = entry
	return entry, true
}

func (cache *ResponseCache) invalidate() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 0 {
		cache.entries = make(map[string]cachedResponse)
	}
}

func (cache *ResponseCache) write(response http.ResponseWriter, request *http.Request, entry cachedResponse) {
	if entry.contentType != "" {
		response.Header().Set("Content-Type", entry.contentType)
	}
	response.Header().Set("ETag", entry.etag)
	response.Header().Set("Cache-Control", "private, max-age=5")
	if match := request.Header.Get("If-None-Match"); match != "" && strings.Contains(match, entry.etag) {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	response.WriteHeader(entry.status)
	_, _ = response.Write(entry.body)
}

// bufferingWriter captures one handler response before the cache decides
// whether to keep it.
type bufferingWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferingWriter() *bufferingWriter {
	return &bufferingWriter{header: make(http.Header), status: http.StatusOK}
}

func (writer *bufferingWriter) Header() http.Header { return writer.header }

func (writer *bufferingWriter) WriteHeader(status int) { writer.status = status }

func (writer *bufferingWriter) Write(data []byte) (int, error) { return writer.body.Write(data) }

func (writer *bufferingWriter) flush(response http.ResponseWriter) {
	for key, values := range writer.header {
		for _, value := range values {
			response.Header().Add(key, value)
		}
	}
	response.WriteHeader(writer.status)
	_, _ = response.Write(writer.body.Bytes())
}
