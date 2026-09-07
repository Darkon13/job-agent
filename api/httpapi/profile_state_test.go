package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage/memory"
	"github.com/Darkon13/job-agent/workflow"
)

type profileStateAPIClock struct{ now time.Time }

func (clock profileStateAPIClock) Now() time.Time { return clock.now }

type profileStateAPIIDs struct{ next int }

func (ids *profileStateAPIIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}

type profileStateAPIReader func(context.Context, adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error)

func (reader profileStateAPIReader) ReadProfileState(ctx context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
	return reader(ctx, request)
}

func TestProfileStateAPIListsMetadataAndCreatesRedactedPlan(t *testing.T) {
	now := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"desired secret"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	repository := memory.NewRepository()
	queue := brokermemory.NewQueue()
	planner, err := workflow.NewProfileStatePlanner([]core.ProfileStateResource{resource}, repository, profileStateAPIClock{now}, &profileStateAPIIDs{})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}
	reads := 0
	reader := profileStateAPIReader(func(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		reads++
		if request.ProfileID != "primary" || len(request.Paths) != 1 || request.Paths[0] != "/resumes/resume-1/about" {
			t.Fatalf("read request = %#v", request)
		}
		return core.NewProfileStateObservation(request.ProfileID, json.RawMessage(`{"resumes":{"resume-1":{"about":"current secret"}}}`), "", now)
	})
	apply, err := workflow.NewProfileStateApplyWorkflow(repository, queue, profileStateAPIClock{now}, &profileStateAPIIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	if err != nil {
		t.Fatalf("new apply workflow: %v", err)
	}
	api, err := NewProfileStateAPI(planner, apply, repository, map[core.ProfileID]adapter.ProfileStateReader{"primary": reader})
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	handler := api.Handler(nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profile-state/resources", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"paths":["/resumes/resume-1/about"]`) || !strings.Contains(response.Body.String(), `"readable":true`) || !strings.Contains(response.Body.String(), `"writable":true`) {
		t.Fatalf("resources response: %d %s", response.Code, response.Body.String())
	}
	assertProfileStateSecretsAbsent(t, response.Body.String())

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/resources/backend/plans", nil))
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"status":"planned"`) || reads != 1 {
		t.Fatalf("plan response: %d reads=%d %s", response.Code, reads, response.Body.String())
	}
	assertProfileStateSecretsAbsent(t, response.Body.String())

	var created core.ProfileStateProposal
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/proposals/"+string(created.ID)+"/apply", nil))
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"type":"profile_state.apply"`) {
		t.Fatalf("apply response: %d %s", response.Code, response.Body.String())
	}
	assertProfileStateSecretsAbsent(t, response.Body.String())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/proposals/"+string(created.ID)+"/apply", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("idempotent apply response: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profile-state/proposals/"+string(created.ID), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("get proposal response: %d %s", response.Code, response.Body.String())
	}
	assertProfileStateSecretsAbsent(t, response.Body.String())

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/resources/backend/plans", nil))
	if response.Code != http.StatusOK || reads != 2 {
		t.Fatalf("idempotent plan response: %d reads=%d %s", response.Code, reads, response.Body.String())
	}
}

func TestProfileStateAPIRejectsCallerObservationAndUnavailableReader(t *testing.T) {
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"desired secret"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	repository := memory.NewRepository()
	queue := brokermemory.NewQueue()
	planner, err := workflow.NewProfileStatePlanner([]core.ProfileStateResource{resource}, repository, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}
	apply, err := workflow.NewProfileStateApplyWorkflow(repository, queue, workflow.SystemClock{}, workflow.RandomIDGenerator{}, nil)
	if err != nil {
		t.Fatalf("new apply workflow: %v", err)
	}
	api, err := NewProfileStateAPI(planner, apply, repository, nil)
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	handler := api.Handler(nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/resources/backend/plans", strings.NewReader(`{"state":{}}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("body status: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/resources/backend/plans", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("reader status: %d %s", response.Code, response.Body.String())
	}
}

func assertProfileStateSecretsAbsent(t *testing.T, value string) {
	t.Helper()
	for _, secret := range []string{"desired secret", "current secret"} {
		if strings.Contains(value, secret) {
			t.Fatalf("response leaked %q: %s", secret, value)
		}
	}
}
