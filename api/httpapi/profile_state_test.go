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
	reconcile, _ := workflow.NewProfileStateReconcileWorkflow(planner, queue, profileStateAPIClock{now}, &profileStateAPIIDs{next: 100}, map[core.ProfileID]core.Platform{"primary": "hh"})
	api, err := NewProfileStateAPI(planner, apply, reconcile, repository, map[core.ProfileID]adapter.ProfileStateReader{"primary": reader})
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	handler := api.Handler(nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profile-state/resources", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"paths":["/resumes/resume-1/about"]`) || !strings.Contains(response.Body.String(), `"editable_paths":["/resumes/resume-1/about"]`) || !strings.Contains(response.Body.String(), `"readable":true`) || !strings.Contains(response.Body.String(), `"writable":true`) || !strings.Contains(response.Body.String(), `"reconcilable":true`) {
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

	reconcileRequest := httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/resources/backend/reconcile", nil)
	reconcileRequest.Header.Set("Idempotency-Key", "reconcile-1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, reconcileRequest)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"type":"profile_state.reconcile"`) {
		t.Fatalf("reconcile response: %d %s", response.Code, response.Body.String())
	}
	assertProfileStateSecretsAbsent(t, response.Body.String())
	reconcileRequest = httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/resources/backend/reconcile", nil)
	reconcileRequest.Header.Set("Idempotency-Key", "reconcile-1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, reconcileRequest)
	if response.Code != http.StatusOK {
		t.Fatalf("idempotent reconcile response: %d %s", response.Code, response.Body.String())
	}
}

func TestProfileStateAPIPlansOneShotEditorOverrideWithoutLeakingIt(t *testing.T) {
	now := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"from config"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	repository := memory.NewRepository()
	planner, err := workflow.NewProfileStatePlanner([]core.ProfileStateResource{resource}, repository, profileStateAPIClock{now}, &profileStateAPIIDs{})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}
	reader := profileStateAPIReader(func(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		return core.NewProfileStateObservation(request.ProfileID, json.RawMessage(`{"resumes":{"resume-1":{"about":"current"}}}`), "", now)
	})
	queue := brokermemory.NewQueue()
	apply, err := workflow.NewProfileStateApplyWorkflow(repository, queue, profileStateAPIClock{now}, &profileStateAPIIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	if err != nil {
		t.Fatalf("new apply workflow: %v", err)
	}
	reconcile, _ := workflow.NewProfileStateReconcileWorkflow(planner, queue, profileStateAPIClock{now}, &profileStateAPIIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	api, err := NewProfileStateAPI(planner, apply, reconcile, repository, map[core.ProfileID]adapter.ProfileStateReader{"primary": reader})
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	handler := api.Handler(nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profile-state/resources/backend/editor", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"value":"from config"`) {
		t.Fatalf("editor response: %d %s", response.Code, response.Body.String())
	}

	body := fmt.Sprintf(`{"base_manifest_digest":%q,"overrides":[{"path":"/resumes/resume-1/about","value":"from dashboard"}]}`, resource.ManifestDigest)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/resources/backend/plans", strings.NewReader(body)))
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"status":"planned"`) {
		t.Fatalf("override plan response: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "from dashboard") || strings.Contains(response.Body.String(), "current") {
		t.Fatalf("plan response leaked state: %s", response.Body.String())
	}
	var redacted core.ProfileStateProposal
	if err := json.Unmarshal(response.Body.Bytes(), &redacted); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	stored, err := repository.ProfileStateProposal(context.Background(), redacted.ID)
	if err != nil || !strings.Contains(string(stored.DesiredState), "from dashboard") {
		t.Fatalf("stored override = %s err=%v", stored.DesiredState, err)
	}
	registered, _ := planner.Resource("backend")
	if string(registered.State) != string(resource.State) {
		t.Fatalf("registered source changed: %s", registered.State)
	}
}

func TestProfileStateAPIRejectsStaleOrUnsupportedEditorOverride(t *testing.T) {
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"from config","title":"Backend"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	repository := memory.NewRepository()
	planner, _ := workflow.NewProfileStatePlanner([]core.ProfileStateResource{resource}, repository, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	queue := brokermemory.NewQueue()
	apply, _ := workflow.NewProfileStateApplyWorkflow(repository, queue, workflow.SystemClock{}, workflow.RandomIDGenerator{}, nil)
	reconcile, _ := workflow.NewProfileStateReconcileWorkflow(planner, queue, workflow.SystemClock{}, workflow.RandomIDGenerator{}, nil)
	reader := profileStateAPIReader(func(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		return core.NewProfileStateObservation(request.ProfileID, resource.State, "", time.Now().UTC())
	})
	api, _ := NewProfileStateAPI(planner, apply, reconcile, repository, map[core.ProfileID]adapter.ProfileStateReader{"primary": reader})
	handler := api.Handler(nil)

	for _, test := range []struct {
		body string
		want int
	}{
		{body: `{"base_manifest_digest":"stale","overrides":[{"path":"/resumes/resume-1/about","value":"new"}]}`, want: http.StatusConflict},
		{body: fmt.Sprintf(`{"base_manifest_digest":%q,"overrides":[{"path":"/resumes/resume-1/title","value":"new"}]}`, resource.ManifestDigest), want: http.StatusUnprocessableEntity},
		{body: fmt.Sprintf(`{"base_manifest_digest":%q,"overrides":[{"path":"/resumes/resume-1/about","value":42}]}`, resource.ManifestDigest), want: http.StatusUnprocessableEntity},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/resources/backend/plans", strings.NewReader(test.body)))
		if response.Code != test.want {
			t.Fatalf("body %s: status %d, want %d: %s", test.body, response.Code, test.want, response.Body.String())
		}
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
	reconcile, _ := workflow.NewProfileStateReconcileWorkflow(planner, queue, workflow.SystemClock{}, workflow.RandomIDGenerator{}, nil)
	api, err := NewProfileStateAPI(planner, apply, reconcile, repository, nil)
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
