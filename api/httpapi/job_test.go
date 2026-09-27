package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/scheduler"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

type jobSchedules []scheduler.Entry

func (schedules jobSchedules) Schedules(context.Context) ([]scheduler.Entry, error) {
	return schedules, nil
}

type jobPausesFake struct{ items []storage.JobPause }

func (pauses *jobPausesFake) PauseJob(_ context.Context, jobTag string, profileID core.ProfileID, reason string, now time.Time) error {
	pauses.items = append(pauses.items, storage.JobPause{JobTag: jobTag, ProfileID: profileID, Reason: reason, CreatedAt: now})
	return nil
}

func (pauses *jobPausesFake) PauseJobs(_ context.Context, jobTag string, reason string, now time.Time) (int, error) {
	pauses.items = append(pauses.items, storage.JobPause{JobTag: jobTag, ProfileID: "primary", Reason: reason, CreatedAt: now})
	return 1, nil
}

func (pauses *jobPausesFake) ResumeJob(_ context.Context, jobTag string, profileID core.ProfileID) (int, error) {
	kept := pauses.items[:0]
	removed := 0
	for _, item := range pauses.items {
		if item.JobTag == jobTag && (profileID == "" || item.ProfileID == profileID) {
			removed++
			continue
		}
		kept = append(kept, item)
	}
	pauses.items = kept
	return removed, nil
}

func (pauses *jobPausesFake) ListJobPauses(context.Context) ([]storage.JobPause, error) {
	return pauses.items, nil
}

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
		Tag: "daily-applications",
		Commands: []workflow.JobRunCommand{{
			TaskType: core.TaskApplicationCampaign, Platform: "hh", ProfileID: "primary",
			Payload: json.RawMessage(`{"job_tag":"daily-applications"}`), Priority: 100,
		}},
	}})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	api, err := NewJobAPI(jobWorkflow, nil)
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	handler := api.Handler(nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"tag":"daily-applications"`) || !strings.Contains(response.Body.String(), `"payload":{"job_tag":"daily-applications"}`) {
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

func TestJobAPIPausesAndResumesJobs(t *testing.T) {
	jobWorkflow, err := workflow.NewJobRunWorkflow(brokermemory.NewQueue(), jobClock{now: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}, &jobIDs{}, []workflow.JobRunDefinition{{
		Tag: "state-harvest.chats",
		Commands: []workflow.JobRunCommand{{
			TaskType: core.TaskConversationDiscover, Platform: "hh", ProfileID: "primary",
			Payload: json.RawMessage(`{"profile_id":"primary"}`),
		}},
	}})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	pauses := &jobPausesFake{}
	api, err := NewJobAPI(jobWorkflow, nil)
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	api.SetPauses(pauses)
	handler := api.Handler(nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/state-harvest.chats/pause", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"paused":true`) {
		t.Fatalf("pause job: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	if !strings.Contains(response.Body.String(), `"paused":true`) || !strings.Contains(response.Body.String(), `"reason":"operator"`) {
		t.Fatalf("paused job was not listed as paused: %s", response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/state-harvest.chats/resume", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"paused":false`) {
		t.Fatalf("resume job: %d %s", response.Code, response.Body.String())
	}
	if len(pauses.items) != 0 {
		t.Fatalf("pauses were not cleared: %#v", pauses.items)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/unknown/pause", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown job pause: %d %s", response.Code, response.Body.String())
	}
}

func TestJobAPIPausesEveryJobOfAGroup(t *testing.T) {
	jobWorkflow, err := workflow.NewJobRunWorkflow(brokermemory.NewQueue(), jobClock{now: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}, &jobIDs{}, []workflow.JobRunDefinition{
		{
			Tag: "daily-applications",
			Commands: []workflow.JobRunCommand{{
				TaskType: core.TaskApplicationCampaign, Platform: "hh", ProfileID: "primary",
				Payload: json.RawMessage(`{"job_tag":"daily-applications"}`),
			}},
		},
		{
			Tag: "system.state.chats",
			Commands: []workflow.JobRunCommand{{
				TaskType: core.TaskConversationDiscover, Platform: "hh", ProfileID: "primary",
				Payload: json.RawMessage(`{"profile_id":"primary"}`),
			}},
		},
	})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	pauses := &jobPausesFake{}
	api, err := NewJobAPI(jobWorkflow, nil)
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	api.SetPauses(pauses)
	api.SetSystemTags([]string{"system.state.chats"})
	handler := api.Handler(nil)

	// System jobs are paused by the service, not by the operator.
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/pause?group=system", nil))
	if response.Code != http.StatusConflict || len(pauses.items) != 0 {
		t.Fatalf("pause system group: %d %s pauses=%#v", response.Code, response.Body.String(), pauses.items)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/system.state.chats/pause", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("pause system tag: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/pause?group=user", nil))
	if response.Code != http.StatusOK || len(pauses.items) != 1 || pauses.items[0].JobTag != "daily-applications" {
		t.Fatalf("pause user group: %d %s pauses=%#v", response.Code, response.Body.String(), pauses.items)
	}
	// Resuming stays available as the recovery path.
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/resume?group=system", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("resume system group: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/pause?group=unknown", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown group: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	if !strings.Contains(response.Body.String(), `"tag":"system.state.chats"`) || !strings.Contains(response.Body.String(), `"system":true`) {
		t.Fatalf("system job was not flagged: %s", response.Body.String())
	}
}

func TestJobAPIRequiresKeyAndRunnableJob(t *testing.T) {
	jobWorkflow, err := workflow.NewJobRunWorkflow(brokermemory.NewQueue(), jobClock{now: time.Now().UTC()}, &jobIDs{}, nil)
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	api, _ := NewJobAPI(jobWorkflow, nil)
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

func TestJobAPIEnrichesRunnableJobsWithSchedules(t *testing.T) {
	queue := brokermemory.NewQueue()
	jobWorkflow, err := workflow.NewJobRunWorkflow(queue, jobClock{now: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}, &jobIDs{}, []workflow.JobRunDefinition{{
		Tag: "periodic-applications",
		Commands: []workflow.JobRunCommand{{
			TaskType: core.TaskApplicationCampaign, Platform: "hh", ProfileID: "primary",
			Payload: json.RawMessage(`{"target_successful":200}`), Priority: 100,
		}},
	}})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	next := time.Date(2026, 9, 13, 16, 0, 0, 0, time.UTC)
	api, err := NewJobAPI(jobWorkflow, jobSchedules{{
		NextRunAt: next,
		Definition: scheduler.Definition{
			JobTag: "periodic-applications", TriggerIndex: 0, Expression: "0 */4 * * *", Timezone: "Europe/Moscow",
			ActionType: core.TaskApplicationCampaign, JitterMin: time.Minute, JitterMax: 10 * time.Minute,
		},
	}})
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	response := httptest.NewRecorder()
	api.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"expression":"0 */4 * * *"`) ||
		!strings.Contains(body, `"next_run_at":"2026-09-13T16:00:00Z"`) ||
		!strings.Contains(body, `"jitter_min":"1m0s"`) || !strings.Contains(body, `"jitter_max":"10m0s"`) {
		t.Fatalf("list jobs with schedules: %d %s", response.Code, body)
	}
}
