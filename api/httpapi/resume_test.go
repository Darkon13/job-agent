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

func TestResumeAPIExposesConfiguredTargets(t *testing.T) {
	api, err := NewResumeAPI(map[core.ProfileID][]core.ResumeTarget{
		"primary": {
			{ID: "resume-1", Primary: true},
			{ID: "resume-2", Alias: "front"},
		},
	})
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	handler := api.Handler(nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profiles/primary/resumes", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body listResponse[core.ResumeTarget]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 2 || !body.Items[0].Primary || body.Items[1].Alias != "front" {
		t.Fatalf("items = %#v", body.Items)
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/v1/profiles/other/resumes", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown profile status = %d", missing.Code)
	}
}

func TestResumeAPIEnqueuesIdempotentUpdate(t *testing.T) {
	api, err := NewResumeAPI(map[core.ProfileID][]core.ResumeTarget{"primary": {{ID: "resume-1", Primary: true}}})
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	workflow, err := workflow.NewResumeUpdateWorkflow(brokermemory.NewQueue(), &apiClock{now: time.Now().UTC()}, &apiIDs{})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	api.ConfigureUpdate(workflow)
	handler := api.Handler(nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/primary/resumes/resume-1/update", strings.NewReader(`{"resource_tag":"primary-resume","publish":false}`))
	request.Header.Set("Idempotency-Key", "update-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var task taskResponse
	if err := json.Unmarshal(response.Body.Bytes(), &task); err != nil || !task.Created || task.TaskID == "" {
		t.Fatalf("task = %#v err=%v", task, err)
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, httptest.NewRequest(http.MethodPost, "/api/v1/profiles/primary/resumes/resume-1/update", strings.NewReader(`{"resource_tag":"primary-resume","publish":false}`)))
	// A replay without the key must fail, and with the key must deduplicate.
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("missing key status = %d", replay.Code)
	}
	replay = httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/primary/resumes/resume-1/update", strings.NewReader(`{"resource_tag":"primary-resume","publish":false}`))
	replayRequest.Header.Set("Idempotency-Key", "update-1")
	handler.ServeHTTP(replay, replayRequest)
	var repeated taskResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &repeated); err != nil || repeated.Created || repeated.TaskID != task.TaskID {
		t.Fatalf("replay = %#v err=%v", repeated, err)
	}
}
