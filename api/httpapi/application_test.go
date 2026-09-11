package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
	"github.com/Darkon13/job-agent/workflow"
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
	api, err := NewApplicationAPI(removal)
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
	api, _ := NewApplicationAPI(removal)
	response := performRequest(t, api.Handler(nil), http.MethodPost, "/api/v1/applications/remove", "remove-empty", "", removeApplicationsRequest{})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("empty selection status: %d body=%s", response.Code, response.Body.String())
	}
}
