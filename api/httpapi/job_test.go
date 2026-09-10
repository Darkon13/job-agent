package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/workflow"
)

type jobClock struct{ now time.Time }

func (clock jobClock) Now() time.Time { return clock.now }

type jobIDs struct{ next int }

func (ids *jobIDs) NewID(prefix string) (string, error) {
	ids.next++
	return prefix + "-id-" + string(rune('0'+ids.next)), nil
}

func TestJobAPIListsAndQueuesRuns(t *testing.T) {
	queue := brokermemory.NewQueue()
	jobWorkflow, err := workflow.NewJobRunWorkflow(queue, jobClock{now: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}, &jobIDs{}, []workflow.JobRunDefinition{{
		Tag: "daily-applications", TaskType: core.TaskApplicationCampaign, Platform: "hh", ProfileID: "primary",
		Payload: json.RawMessage(`{"job_tag":"daily-applications"}`), Priority: 100,
	}})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	api, err := NewJobAPI(jobWorkflow)
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	handler := api.Handler(nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"tag":"daily-applications"`) || strings.Contains(response.Body.String(), "job_tag") {
		t.Fatalf("list jobs: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/daily-applications/runs", nil)
	request.Header.Set("Idempotency-Key", "run-1")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"created":true`) {
		t.Fatalf("run job: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/jobs/daily-applications/runs", nil)
	request.Header.Set("Idempotency-Key", "run-1")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"created":false`) {
		t.Fatalf("repeat job: %d %s", response.Code, response.Body.String())
	}
}

func TestJobAPIRequiresKeyAndRunnableJob(t *testing.T) {
	jobWorkflow, err := workflow.NewJobRunWorkflow(brokermemory.NewQueue(), jobClock{now: time.Now().UTC()}, &jobIDs{}, nil)
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	api, _ := NewJobAPI(jobWorkflow)
	response := httptest.NewRecorder()
	api.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/missing/runs", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/missing/runs", nil)
	request.Header.Set("Idempotency-Key", "run-1")
	api.Handler(nil).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing job: %d %s", response.Code, response.Body.String())
	}
}
