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
	"github.com/Darkon13/job-agent/broker"
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
	api, err := NewProfileStateAPI(planner, apply, reconcile, repository, repository, map[core.ProfileID]adapter.ProfileStateReader{"primary": reader})
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
	lease, found, err := queue.Claim(context.Background(), broker.ClaimParams{
		WorkerID: "profile-worker", TaskType: core.TaskProfileStateApply,
		Now: now, LeaseDuration: time.Minute,
	})
	if err != nil || !found {
		t.Fatalf("claim failed apply fixture: found=%t err=%v", found, err)
	}
	if err := queue.Fail(context.Background(), lease, &core.OperationError{
		Category: core.ErrorPermanentFailure, Operation: "profile_state.apply", Message: "invalid field",
	}, now); err != nil {
		t.Fatalf("fail apply fixture: %v", err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/proposals/"+string(created.ID)+"/retry", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"new"`) || !strings.Contains(response.Body.String(), `"attempts":0`) {
		t.Fatalf("retry response: %d %s", response.Code, response.Body.String())
	}
	lease, found, err = queue.Claim(context.Background(), broker.ClaimParams{
		WorkerID: "profile-worker", TaskType: core.TaskProfileStateApply,
		Now: now, LeaseDuration: time.Minute,
	})
	if err != nil || !found {
		t.Fatalf("claim retried apply fixture: found=%t err=%v", found, err)
	}
	if err := queue.Fail(context.Background(), lease, &core.OperationError{
		Category: core.ErrorPermanentFailure, Operation: "profile_state.apply", Message: "still invalid",
	}, now); err != nil {
		t.Fatalf("fail retried apply fixture: %v", err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/proposals/"+string(created.ID)+"/dismiss", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"dismissed"`) {
		t.Fatalf("dismiss response: %d %s", response.Code, response.Body.String())
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
	api, err := NewProfileStateAPI(planner, apply, reconcile, repository, repository, map[core.ProfileID]adapter.ProfileStateReader{"primary": reader})
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
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"from config","skills":["Go"]}}}`))
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
	api, _ := NewProfileStateAPI(planner, apply, reconcile, repository, repository, map[core.ProfileID]adapter.ProfileStateReader{"primary": reader})
	handler := api.Handler(nil)

	for _, test := range []struct {
		body string
		want int
	}{
		{body: `{"base_manifest_digest":"stale","overrides":[{"path":"/resumes/resume-1/about","value":"new"}]}`, want: http.StatusConflict},
		{body: fmt.Sprintf(`{"base_manifest_digest":%q,"overrides":[{"path":"/resumes/resume-1/skills","value":"Go"}]}`, resource.ManifestDigest), want: http.StatusUnprocessableEntity},
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
	api, err := NewProfileStateAPI(planner, apply, reconcile, repository, repository, nil)
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

func TestProfileStateAPICreatesOneShotBootstrapPlan(t *testing.T) {
	now := time.Date(2026, 9, 8, 9, 30, 0, 0, time.UTC)
	repository := memory.NewRepository()
	queue := brokermemory.NewQueue()
	planner, _ := workflow.NewProfileStatePlanner(nil, repository, profileStateAPIClock{now}, &profileStateAPIIDs{})
	apply, _ := workflow.NewProfileStateApplyWorkflow(repository, queue, profileStateAPIClock{now}, &profileStateAPIIDs{next: 10}, map[core.ProfileID]core.Platform{"primary": "hh"})
	reconcile, _ := workflow.NewProfileStateReconcileWorkflow(planner, queue, profileStateAPIClock{now}, &profileStateAPIIDs{next: 20}, nil)
	reader := profileStateAPIReader(func(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		if request.ProfileID != "primary" || len(request.Paths) != 2 {
			t.Fatalf("read request = %#v", request)
		}
		return core.NewProfileStateObservation(request.ProfileID, json.RawMessage(`{"resumes":{"resume-1":{"experience":[],"skill_set":["Go"]}}}`), "revision-1", now)
	})
	api, _ := NewProfileStateAPI(planner, apply, reconcile, repository, repository, map[core.ProfileID]adapter.ProfileStateReader{"primary": reader})
	body := `{
		"api_version":"job-agent/v1",
		"kind":"ProfileBootstrap",
		"metadata":{"name":"primary-resume"},
		"spec":{"profile_id":"primary","state":{"resumes":{"resume-1":{
			"experience":[{"company":"Example","position":"Developer"}],
			"skill_set":["Go","PostgreSQL"]
		}}}}
	}`
	response := httptest.NewRecorder()
	api.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/bootstrap/plans", strings.NewReader(body)))
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"status":"planned"`) || !strings.Contains(response.Body.String(), `"resource_tag":"primary-resume"`) {
		t.Fatalf("bootstrap plan response: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "Example") || strings.Contains(response.Body.String(), "PostgreSQL") {
		t.Fatalf("bootstrap response leaked desired values: %s", response.Body.String())
	}
}

func TestProfileStateAPIRejectsUnknownBootstrapProfileAndControlField(t *testing.T) {
	repository := memory.NewRepository()
	queue := brokermemory.NewQueue()
	planner, _ := workflow.NewProfileStatePlanner(nil, repository, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	apply, _ := workflow.NewProfileStateApplyWorkflow(repository, queue, workflow.SystemClock{}, workflow.RandomIDGenerator{}, nil)
	reconcile, _ := workflow.NewProfileStateReconcileWorkflow(planner, queue, workflow.SystemClock{}, workflow.RandomIDGenerator{}, nil)
	api, _ := NewProfileStateAPI(planner, apply, reconcile, repository, repository, nil)
	for _, body := range []string{
		`{"api_version":"job-agent/v1","kind":"ProfileBootstrap","metadata":{"name":"resume"},"spec":{"profile_id":"missing","state":{"profile":{"area":"1"}}}}`,
		`{"api_version":"job-agent/v1","kind":"ProfileBootstrap","metadata":{"name":"resume"},"spec":{"profile_id":"missing","state":{"profile":{"area":"1"}}},"typo":true}`,
	} {
		response := httptest.NewRecorder()
		api.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/bootstrap/plans", strings.NewReader(body)))
		if response.Code < 400 {
			t.Fatalf("bootstrap should fail: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestProfileStateAPIListsRedactedRevisions(t *testing.T) {
	now := time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"revision secret new"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	before, err := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"about":"revision secret old"}}}`), "", now)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	proposal, err := core.NewProfileStateProposal("proposal-1", resource, before, now)
	if err != nil {
		t.Fatalf("new proposal: %v", err)
	}
	revision, err := core.NewProfileStateRevision(proposal, "resume-api", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("new revision: %v", err)
	}
	repository := memory.NewRepository()
	if _, _, err := repository.CreateProfileStateProposal(context.Background(), proposal); err != nil {
		t.Fatalf("store proposal: %v", err)
	}
	if _, created, err := repository.CreateProfileStateRevision(context.Background(), revision); err != nil || !created {
		t.Fatalf("store revision: created=%t err=%v", created, err)
	}
	queue := brokermemory.NewQueue()
	planner, err := workflow.NewProfileStatePlanner([]core.ProfileStateResource{resource}, repository, profileStateAPIClock{now}, &profileStateAPIIDs{})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}
	apply, err := workflow.NewProfileStateApplyWorkflow(repository, queue, profileStateAPIClock{now}, &profileStateAPIIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	if err != nil {
		t.Fatalf("new apply workflow: %v", err)
	}
	reconcile, _ := workflow.NewProfileStateReconcileWorkflow(planner, queue, profileStateAPIClock{now}, &profileStateAPIIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	api, err := NewProfileStateAPI(planner, apply, reconcile, repository, repository, nil)
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	handler := api.Handler(nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profile-state/revisions?resource_tag=backend", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("revisions response: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "revision secret") {
		t.Fatalf("revisions response leaked values: %s", response.Body.String())
	}
	var body listResponse[core.ProfileStateRevision]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode revisions: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].ProposalID != "proposal-1" || body.Items[0].Source != "resume-api" || len(body.Items[0].Changes) != 1 {
		t.Fatalf("revisions = %#v", body.Items)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profile-state/revisions?profile_id=secondary", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatalf("filtered revisions response: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profile-state/revisions?limit=999", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit response: %d %s", response.Code, response.Body.String())
	}
}

func TestProfileStateAPIEditorExposesDeclaredTextFieldsOnly(t *testing.T) {
	now := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{
		"resumes": {"resume-1": {
			"about": "legacy text",
			"web": {"about": "web text", "phone": "79990000000", "photo": null, "skills": ["Go"], "title": ["Backend"]},
			"web_profile": {"firstName": ["Иван"]}
		}}
	}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	repository := memory.NewRepository()
	queue := brokermemory.NewQueue()
	planner, err := workflow.NewProfileStatePlanner([]core.ProfileStateResource{resource}, repository, profileStateAPIClock{now}, &profileStateAPIIDs{})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}
	reader := profileStateAPIReader(func(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		return core.NewProfileStateObservation("primary", resource.State, "", now)
	})
	apply, err := workflow.NewProfileStateApplyWorkflow(repository, queue, profileStateAPIClock{now}, &profileStateAPIIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	if err != nil {
		t.Fatalf("new apply workflow: %v", err)
	}
	reconcile, _ := workflow.NewProfileStateReconcileWorkflow(planner, queue, profileStateAPIClock{now}, &profileStateAPIIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	api, err := NewProfileStateAPI(planner, apply, reconcile, repository, repository, map[core.ProfileID]adapter.ProfileStateReader{"primary": reader})
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	handler := api.Handler(nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profile-state/resources/backend/editor", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("editor response: %d %s", response.Code, response.Body.String())
	}
	var editor ProfileStateResourceEditor
	if err := json.Unmarshal(response.Body.Bytes(), &editor); err != nil {
		t.Fatalf("decode editor: %v", err)
	}
	want := []string{
		"/resumes/resume-1/about", "/resumes/resume-1/web/about",
		"/resumes/resume-1/web/phone", "/resumes/resume-1/web/photo",
	}
	if len(editor.Fields) != len(want) {
		t.Fatalf("editable fields = %#v", editor.Fields)
	}
	for index, field := range editor.Fields {
		if field.Path != want[index] {
			t.Fatalf("editable fields = %#v", editor.Fields)
		}
	}
	if editor.Fields[3].Value != nil {
		t.Fatalf("null field = %#v", editor.Fields[3])
	}

	body := fmt.Sprintf(`{"base_manifest_digest":%q,"overrides":[{"path":"/resumes/resume-1/web/skills","value":"Go"}]}`, resource.ManifestDigest)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/resources/backend/plans", strings.NewReader(body)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("array override response: %d %s", response.Code, response.Body.String())
	}
	body = fmt.Sprintf(`{"base_manifest_digest":%q,"overrides":[{"path":"/resumes/resume-1/web/phone","value":"70000000000"}]}`, resource.ManifestDigest)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile-state/resources/backend/plans", strings.NewReader(body)))
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"status":"planned"`) {
		t.Fatalf("text override response: %d %s", response.Code, response.Body.String())
	}
}
