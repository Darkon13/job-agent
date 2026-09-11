package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
	"github.com/Darkon13/job-agent/workflow"
)

func newQualificationAPI(t *testing.T) (http.Handler, *storagememory.Repository) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	order := 1
	repository := storagememory.NewRepository()
	if err := repository.UpsertQualificationOfferings(ctx, "hh", "primary", []core.QualificationOffering{
		{
			ID: "offer-1", Platform: "hh", ProfileID: "primary", ExternalID: "go-medium",
			Qualification: core.QualificationDescriptor{
				FamilyID: "go", FamilyName: "Go", LevelID: "medium", LevelName: "Средний", LevelOrder: &order,
			},
			Status: core.QualificationAvailable, ObservedAt: now,
		},
	}); err != nil {
		t.Fatalf("seed offerings: %v", err)
	}
	workflow, err := workflow.NewQualificationWorkflow(brokermemory.NewQueue(), &apiClock{now: now}, &apiIDs{})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	api, err := NewQualificationAPI(repository, workflow, map[core.ProfileID]core.Platform{"primary": "hh"})
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	return api.Handler(nil), repository
}

func TestQualificationAPIListAndSync(t *testing.T) {
	handler, _ := newQualificationAPI(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profiles/primary/qualifications", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", response.Code, response.Body.String())
	}
	var body listResponse[core.QualificationOffering]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Items) != 1 {
		t.Fatalf("items = %#v err=%v", body.Items, err)
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/v1/profiles/other/qualifications", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown profile status = %d", missing.Code)
	}

	noKey := httptest.NewRecorder()
	handler.ServeHTTP(noKey, httptest.NewRequest(http.MethodPost, "/api/v1/profiles/primary/qualifications/sync", nil))
	if noKey.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency status = %d", noKey.Code)
	}
	sync := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/primary/qualifications/sync", nil)
	request.Header.Set("Idempotency-Key", "sync-1")
	handler.ServeHTTP(sync, request)
	if sync.Code != http.StatusAccepted {
		t.Fatalf("sync status = %d body=%s", sync.Code, sync.Body.String())
	}
	var task taskResponse
	if err := json.Unmarshal(sync.Body.Bytes(), &task); err != nil || !task.Created {
		t.Fatalf("task = %#v err=%v", task, err)
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, request)
	var repeated taskResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &repeated); err != nil || repeated.Created || repeated.TaskID != task.TaskID {
		t.Fatalf("replay = %#v err=%v", repeated, err)
	}
}

func TestQualificationAPIStartEnqueuesAttempt(t *testing.T) {
	handler, _ := newQualificationAPI(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/primary/qualifications/hh:offer-1/start", nil)
	request.Header.Set("Idempotency-Key", "start-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start status = %d body=%s", response.Code, response.Body.String())
	}
	var task taskResponse
	if err := json.Unmarshal(response.Body.Bytes(), &task); err != nil || !task.Created || task.TaskID == "" {
		t.Fatalf("task = %#v err=%v", task, err)
	}
	replay := httptest.NewRecorder()
	handler.ServeHTTP(replay, request)
	var repeated taskResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &repeated); err != nil || repeated.Created || repeated.TaskID != task.TaskID {
		t.Fatalf("replay = %#v err=%v", repeated, err)
	}
}
