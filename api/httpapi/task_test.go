package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/broker"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

type failedTaskRepositoryStub struct {
	items  []storage.FailedTaskSummary
	queued []storage.QueuedTaskSummary
}

type taskAPIClock struct{ now time.Time }

func (clock taskAPIClock) Now() time.Time { return clock.now }

func (repository failedTaskRepositoryStub) ListFailedTasks(context.Context, int) ([]storage.FailedTaskSummary, error) {
	return repository.items, nil
}

func (repository failedTaskRepositoryStub) ListQueuedTasks(context.Context, int) ([]storage.QueuedTaskSummary, error) {
	return repository.queued, nil
}

func TestTaskAPIListsQueuedTasksAndCancelsThemByID(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, time.UTC)
	queue := brokermemory.NewQueue()
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-queued", Type: core.TaskApplicationSubmit, IdempotencyKey: "queued-key", Source: "test",
		ProfileID: "primary", CorrelationID: "correlation-queued", Payload: json.RawMessage(`{}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if _, err := queue.Enqueue(ctx, task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	control, _ := workflow.NewTaskControlWorkflow(queue, taskAPIClock{now.Add(time.Second)})
	api, err := NewTaskAPI(failedTaskRepositoryStub{queued: []storage.QueuedTaskSummary{{
		ID: task.ID, Type: task.Type, Status: core.TaskNew, ProfileID: task.ProfileID,
		AvailableAt: now, CreatedAt: now,
	}}}, control)
	if err != nil {
		t.Fatalf("new task API: %v", err)
	}
	listResponse := httptest.NewRecorder()
	api.Handler(nil).ServeHTTP(listResponse, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/queued", nil))
	var listing struct {
		Items []storage.QueuedTaskSummary `json:"items"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&listing); err != nil || len(listing.Items) != 1 || listing.Items[0].ID != task.ID {
		t.Fatalf("queued listing = %#v err=%v", listing, err)
	}

	cancel := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-queued/cancel", strings.NewReader(`{"reason":"junk"}`))
	cancel.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	api.Handler(nil).ServeHTTP(recorder, cancel)
	if recorder.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var result TaskControlResult
	if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil || result.Status != core.TaskDismissed {
		t.Fatalf("cancel result = %#v err=%v", result, err)
	}
	// Repeating the cancel is a conflict, not a silent success.
	recorder = httptest.NewRecorder()
	repeat := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-queued/cancel", nil)
	api.Handler(nil).ServeHTTP(recorder, repeat)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("second cancel status = %d", recorder.Code)
	}
}

func TestTaskAPIListsAndControlsFailuresWithoutReturningPayload(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 17, 0, 0, 0, time.UTC)
	queue := brokermemory.NewQueue()
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-1", Type: core.TaskConversationSend, IdempotencyKey: "secret-key", Source: "test",
		ProfileID: "primary", CorrelationID: "correlation-1", Payload: json.RawMessage(`{"text":"secret message"}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if _, err := queue.Enqueue(ctx, task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	lease, found, err := queue.Claim(ctx, broker.ClaimParams{WorkerID: "worker", TaskType: task.Type, Now: now, LeaseDuration: time.Minute})
	if err != nil || !found {
		t.Fatalf("claim: found=%t err=%v", found, err)
	}
	failure := &core.OperationError{Category: core.ErrorUnsupported, Operation: "conversation.send", Message: "transport unavailable"}
	if err := queue.Fail(ctx, lease, failure, now.Add(time.Second)); err != nil {
		t.Fatalf("fail: %v", err)
	}
	control, _ := workflow.NewTaskControlWorkflow(queue, taskAPIClock{now.Add(2 * time.Second)})
	api, err := NewTaskAPI(failedTaskRepositoryStub{items: []storage.FailedTaskSummary{{
		ID: task.ID, Type: task.Type, ProfileID: task.ProfileID, Attempts: 1,
		Failure: core.TaskFailure{Category: failure.Category, Message: failure.Message}, UpdatedAt: now.Add(time.Second),
	}}}, control)
	if err != nil {
		t.Fatalf("new task API: %v", err)
	}
	handler := api.Handler(nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/failed", nil))
	if response.Code != http.StatusOK || !containsAll(response.Body.String(), `"id":"task-1"`, `"category":"unsupported"`) ||
		strings.Contains(response.Body.String(), "secret message") || strings.Contains(response.Body.String(), "secret-key") {
		t.Fatalf("list response: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/dismiss", nil))
	if response.Code != http.StatusOK || !containsAll(response.Body.String(), `"status":"dismissed"`, `"message":"transport unavailable"`) ||
		strings.Contains(response.Body.String(), "secret message") || strings.Contains(response.Body.String(), "secret-key") {
		t.Fatalf("dismiss response: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/task-1/retry", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("retry dismissed response: %d %s", response.Code, response.Body.String())
	}
}
