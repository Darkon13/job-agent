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
	items []storage.FailedTaskSummary
}

type taskAPIClock struct{ now time.Time }

func (clock taskAPIClock) Now() time.Time { return clock.now }

func (repository failedTaskRepositoryStub) ListFailedTasks(context.Context, int) ([]storage.FailedTaskSummary, error) {
	return repository.items, nil
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
