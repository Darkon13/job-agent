package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
	"github.com/Darkon13/job-agent/workflow"
	_ "modernc.org/sqlite"
)

type applicationAPIClock struct{ now time.Time }

func (clock applicationAPIClock) Now() time.Time { return clock.now }

type applicationAPIIDs struct{ next int }

func (ids *applicationAPIIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}

func TestApplicationAPIRemovesSelectedObjectsThroughIdempotentTasks(t *testing.T) {
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	for index := 1; index <= 2; index++ {
		application, err := core.NewApplication(
			core.ApplicationID(fmt.Sprintf("application-%d", index)),
			core.ApplicationKey{ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: fmt.Sprintf("vacancy-%d", index)}},
			now,
		)
		if err != nil {
			t.Fatalf("new application: %v", err)
		}
		if _, _, err := repository.CreateApplication(context.Background(), application); err != nil {
			t.Fatalf("store application: %v", err)
		}
		stored, _ := repository.ApplicationByID(context.Background(), application.ID)
		if err := stored.Transition(core.ApplicationPreparing, now.Add(time.Second)); err != nil {
			t.Fatalf("transition application: %v", err)
		}
		if err := repository.SaveApplication(context.Background(), stored, core.ApplicationNew); err != nil {
			t.Fatalf("save application: %v", err)
		}
		if err := stored.Transition(core.ApplicationDryRun, now.Add(2*time.Second)); err != nil {
			t.Fatalf("transition application: %v", err)
		}
		if err := repository.SaveApplication(context.Background(), stored, core.ApplicationPreparing); err != nil {
			t.Fatalf("save application: %v", err)
		}
	}
	removal, err := workflow.NewApplicationRemovalWorkflow(repository, queue, applicationAPIClock{now: now.Add(time.Minute)}, &applicationAPIIDs{})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	retry, err := workflow.NewApplicationRetryWorkflow(repository, queue, applicationAPIClock{now: now.Add(time.Minute)}, &applicationAPIIDs{next: 100})
	if err != nil {
		t.Fatalf("new retry workflow: %v", err)
	}
	api, err := NewApplicationAPI(removal, retry)
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	body := removeApplicationsRequest{ApplicationIDs: []core.ApplicationID{"application-1", "application-2", "application-1"}}
	response := performRequest(t, api.Handler(nil), http.MethodPost, "/api/v1/applications/remove", "remove-selection-1", "", body)
	if response.Code != http.StatusAccepted {
		t.Fatalf("remove status: %d body=%s", response.Code, response.Body.String())
	}
	var first bulkTaskResponse
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil || first.Matched != 2 || first.Created != 2 || len(first.Tasks) != 2 || len(queue.Tasks()) != 2 {
		t.Fatalf("remove response=%#v tasks=%d err=%v", first, len(queue.Tasks()), err)
	}
	response = performRequest(t, api.Handler(nil), http.MethodPost, "/api/v1/applications/remove", "remove-selection-1", "", body)
	var repeated bulkTaskResponse
	if err := json.Unmarshal(response.Body.Bytes(), &repeated); err != nil || repeated.Created != 0 || len(queue.Tasks()) != 2 {
		t.Fatalf("repeated response=%#v tasks=%d err=%v", repeated, len(queue.Tasks()), err)
	}
	response = performRequest(t, api.Handler(nil), http.MethodPost, "/api/v1/applications/remove", "partial-selection", "", removeApplicationsRequest{ApplicationIDs: []core.ApplicationID{"missing", "application-1"}})
	var partial applicationBulkResult
	if err := json.Unmarshal(response.Body.Bytes(), &partial); err != nil || response.Code != http.StatusAccepted || len(partial.Results) != 2 || partial.Results[0].Error != "not_enqueued" || partial.Results[1].TaskID == "" {
		t.Fatalf("partial batch must report each object: %s err=%v", response.Body.String(), err)
	}
}

func TestApplicationAPIValidatesBulkSelection(t *testing.T) {
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	removal, _ := workflow.NewApplicationRemovalWorkflow(repository, queue, applicationAPIClock{now: time.Now()}, &applicationAPIIDs{})
	retry, _ := workflow.NewApplicationRetryWorkflow(repository, queue, applicationAPIClock{now: time.Now()}, &applicationAPIIDs{})
	api, _ := NewApplicationAPI(removal, retry)
	response := performRequest(t, api.Handler(nil), http.MethodPost, "/api/v1/applications/remove", "remove-empty", "", removeApplicationsRequest{})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("empty selection status: %d body=%s", response.Code, response.Body.String())
	}
}

func TestApplicationAPIRetriesBlockedApplication(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	application, err := core.NewApplication(
		"application-1",
		core.ApplicationKey{ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "vacancy-1"}},
		now,
	)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, _, err := repository.CreateApplication(ctx, application); err != nil {
		t.Fatalf("store application: %v", err)
	}
	stored, _ := repository.ApplicationByID(ctx, application.ID)
	if err := stored.Transition(core.ApplicationPreparing, now.Add(time.Second)); err != nil {
		t.Fatalf("transition preparing: %v", err)
	}
	if err := repository.SaveApplication(ctx, stored, core.ApplicationNew); err != nil {
		t.Fatalf("save preparing: %v", err)
	}
	if err := stored.RecordPreparation("questionnaire_required", "vacancy requires a questionnaire", "", "", now.Add(2*time.Second)); err != nil {
		t.Fatalf("record preparation: %v", err)
	}
	if err := stored.Transition(core.ApplicationWaitingValidation, now.Add(3*time.Second)); err != nil {
		t.Fatalf("transition waiting validation: %v", err)
	}
	if err := repository.SaveApplication(ctx, stored, core.ApplicationPreparing); err != nil {
		t.Fatalf("save waiting validation: %v", err)
	}
	retry, err := workflow.NewApplicationRetryWorkflow(repository, queue, applicationAPIClock{now: now.Add(time.Minute)}, &applicationAPIIDs{})
	if err != nil {
		t.Fatalf("new retry workflow: %v", err)
	}
	api, err := NewApplicationAPI(
		mustRemovalWorkflow(t, repository, queue, now), retry,
	)
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	handler := api.Handler(nil)
	response := performRequest(t, handler, http.MethodPost, "/api/v1/applications/application-1/retry", "retry-1", "", nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry status: %d body=%s", response.Code, response.Body.String())
	}
	if len(queue.Tasks()) != 1 || queue.Tasks()[0].Type != core.TaskApplicationSubmit || queue.Tasks()[0].Source != "manual:application.retry" {
		t.Fatalf("retry tasks = %#v", queue.Tasks())
	}
	retried, err := repository.ApplicationByID(ctx, application.ID)
	if err != nil || retried.Status != core.ApplicationReady || retried.DecisionCode != "" || retried.PreparedAt != nil {
		t.Fatalf("retried application = %#v err=%v", retried, err)
	}
	response = performRequest(t, handler, http.MethodPost, "/api/v1/applications/application-1/retry", "retry-1", "", nil)
	var replay taskResponse
	if err := json.Unmarshal(response.Body.Bytes(), &replay); err != nil || response.Code != http.StatusOK || replay.Created || len(queue.Tasks()) != 1 {
		t.Fatalf("replayed retry = %d %s err=%v", response.Code, response.Body.String(), err)
	}
	response = performRequest(t, handler, http.MethodPost, "/api/v1/applications/application-1/retry", "retry-2", "", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("second retry status: %d body=%s", response.Code, response.Body.String())
	}
	response = performRequest(t, handler, http.MethodPost, "/api/v1/applications/missing/retry", "retry-3", "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing application status: %d body=%s", response.Code, response.Body.String())
	}
}

func mustRemovalWorkflow(t *testing.T, repository *storagememory.Repository, queue *brokermemory.Queue, now time.Time) *workflow.ApplicationRemovalWorkflow {
	t.Helper()
	removal, err := workflow.NewApplicationRemovalWorkflow(repository, queue, applicationAPIClock{now: now.Add(time.Minute)}, &applicationAPIIDs{next: 500})
	if err != nil {
		t.Fatalf("new removal workflow: %v", err)
	}
	return removal
}

func TestApplicationListToleratesMissingVacancy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	vacancy := core.Vacancy{Platform: "hh", ExternalID: "orphan-1", Title: "Legacy vacancy", State: core.VacancyStateOpen, ObservedAt: now}
	if _, err := store.UpsertVacancy(context.Background(), vacancy); err != nil {
		t.Fatalf("store vacancy: %v", err)
	}
	application, err := core.NewApplication("application-orphan", core.ApplicationKey{
		ProfileID: "secondary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "orphan-1"},
	}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, _, err := store.CreateApplication(context.Background(), application); err != nil {
		t.Fatalf("create application: %v", err)
	}
	// Legacy merges imported applications without their vacancy rows by
	// bypassing foreign keys; reproduce that dangling reference.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec("DELETE FROM vacancies WHERE external_id = 'orphan-1'"); err != nil {
		t.Fatalf("drop vacancy: %v", err)
	}
	_ = raw.Close()
	api, err := NewRuntimeAPI(store, nil)
	if err != nil {
		t.Fatalf("new runtime API: %v", err)
	}
	response := httptest.NewRecorder()
	api.Handler(http.NotFoundHandler()).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/applications?limit=10", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload applicationListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].VacancyTitle != "" {
		t.Fatalf("list response = %#v", payload)
	}
}

type questionnaireCaptureRecorder struct {
	profileID core.ProfileID
	vacancyID string
	calls     int
}

func (recorder *questionnaireCaptureRecorder) EnqueueCapture(_ context.Context, profileID core.ProfileID, _ core.Platform, externalID string, _ string) (bool, error) {
	recorder.calls++
	recorder.profileID = profileID
	recorder.vacancyID = externalID
	return true, nil
}

func TestApplicationAPIEnqueuesQuestionnaireCapture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repository, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	application, err := core.NewApplication(
		"application-1",
		core.ApplicationKey{ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"}},
		now,
	)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, err := repository.UpsertVacancy(context.Background(), core.Vacancy{
		Platform: "hh", ExternalID: "42", Title: "Go developer", Employer: "Example",
		State: core.VacancyStateOpen, ObservedAt: now,
	}); err != nil {
		t.Fatalf("store vacancy: %v", err)
	}
	if _, _, err := repository.CreateApplication(context.Background(), application); err != nil {
		t.Fatalf("store application: %v", err)
	}
	api, err := NewRuntimeAPI(repository, nil)
	if err != nil {
		t.Fatalf("new runtime API: %v", err)
	}
	recorder := &questionnaireCaptureRecorder{}
	api.ConfigureQuestionnaireCapture(recorder)

	response := performRequest(t, api.Handler(nil), http.MethodPost, "/api/v1/applications/application-1/questionnaire", "questionnaire-1", "", nil)
	if response.Code != http.StatusAccepted || recorder.calls != 1 || recorder.profileID != "primary" || recorder.vacancyID != "42" {
		t.Fatalf("capture response=%d recorder=%#v body=%s", response.Code, recorder, response.Body.String())
	}
	response = performRequest(t, api.Handler(nil), http.MethodPost, "/api/v1/applications/missing/questionnaire", "questionnaire-2", "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing application status=%d body=%s", response.Code, response.Body.String())
	}
}
