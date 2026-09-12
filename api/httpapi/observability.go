package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Darkon13/job-agent/storage"
)

type requestIDKey struct{}

// RequestID ensures every request carries a correlation ID. An incoming
// X-Request-ID header is preserved; otherwise a random one is generated. The
// ID is echoed in the response and available through RequestIDFromContext.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestID := strings.TrimSpace(request.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = newRequestID()
		}
		response.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), requestIDKey{}, requestID)))
	})
}

func RequestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

func newRequestID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(value[:])
}

// AccessLog writes one structured JSON line per HTTP request.
func AccessLog(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		return next
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: response, status: http.StatusOK}
		next.ServeHTTP(recorder, request)
		logger.Info("http_request",
			"method", request.Method,
			"path", request.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
			"request_id", RequestIDFromContext(request.Context()),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

// TaskCountProvider exposes the operator-safe queue summary for metrics.
type TaskCountProvider interface {
	TaskCounts(ctx context.Context) ([]storage.TaskCount, error)
}

// MetricsAPI exposes small process counters in Prometheus text format. It is a
// wrapper so the counters observe every request that passed bearer
// authentication; requests rejected before it stay in the access log.
type MetricsAPI struct {
	started   time.Time
	tasks     TaskCountProvider
	inFlight  atomic.Int64
	status2xx atomic.Uint64
	status4xx atomic.Uint64
	status5xx atomic.Uint64
}

func NewMetricsAPI(tasks TaskCountProvider) *MetricsAPI {
	return &MetricsAPI{started: time.Now().UTC(), tasks: tasks}
}

func (api *MetricsAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", api.serveMetrics)
	mux.Handle("/", next)
	return api.middleware(mux)
}

func (api *MetricsAPI) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		api.inFlight.Add(1)
		defer api.inFlight.Add(-1)
		recorder := &statusRecorder{ResponseWriter: response, status: http.StatusOK}
		next.ServeHTTP(recorder, request)
		switch {
		case recorder.status >= 500:
			api.status5xx.Add(1)
		case recorder.status >= 400:
			api.status4xx.Add(1)
		default:
			api.status2xx.Add(1)
		}
	})
}

func (api *MetricsAPI) serveMetrics(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(response, "# HELP job_agent_http_requests_total HTTP requests handled.\n")
	fmt.Fprintf(response, "# TYPE job_agent_http_requests_total counter\n")
	fmt.Fprintf(response, "job_agent_http_requests_total{class=\"2xx\"} %d\n", api.status2xx.Load())
	fmt.Fprintf(response, "job_agent_http_requests_total{class=\"4xx\"} %d\n", api.status4xx.Load())
	fmt.Fprintf(response, "job_agent_http_requests_total{class=\"5xx\"} %d\n", api.status5xx.Load())
	fmt.Fprintf(response, "# HELP job_agent_http_in_flight Requests currently being served.\n")
	fmt.Fprintf(response, "# TYPE job_agent_http_in_flight gauge\n")
	fmt.Fprintf(response, "job_agent_http_in_flight %d\n", api.inFlight.Load())
	fmt.Fprintf(response, "# HELP job_agent_uptime_seconds Process uptime.\n")
	fmt.Fprintf(response, "# TYPE job_agent_uptime_seconds gauge\n")
	fmt.Fprintf(response, "job_agent_uptime_seconds %d\n", int64(time.Since(api.started).Seconds()))
	if api.tasks == nil {
		return
	}
	counts, err := api.tasks.TaskCounts(request.Context())
	if err != nil {
		fmt.Fprintf(response, "# HELP job_agent_task_counts_error Queue summary could not be read.\n")
		fmt.Fprintf(response, "# TYPE job_agent_task_counts_error gauge\n")
		fmt.Fprintf(response, "job_agent_task_counts_error 1\n")
		return
	}
	fmt.Fprintf(response, "# HELP job_agent_tasks Queued tasks by type, status and priority.\n")
	fmt.Fprintf(response, "# TYPE job_agent_tasks gauge\n")
	for _, count := range counts {
		fmt.Fprintf(response, "job_agent_tasks{type=%q,status=%q,priority=\"%d\"} %d\n",
			count.Type, count.Status, count.Priority, count.Count)
	}
}
