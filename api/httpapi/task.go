package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

type FailedTaskReadRepository interface {
	ListFailedTasks(context.Context, int) ([]storage.FailedTaskSummary, error)
}

type TaskAPI struct {
	repository FailedTaskReadRepository
	control    *workflow.TaskControlWorkflow
}

type TaskControlResult struct {
	ID          core.TaskID       `json:"id"`
	Type        core.TaskType     `json:"type"`
	Status      core.TaskStatus   `json:"status"`
	ProfileID   core.ProfileID    `json:"profile_id,omitempty"`
	Attempts    int               `json:"attempts"`
	AvailableAt time.Time         `json:"available_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Failure     *core.TaskFailure `json:"failure,omitempty"`
}

func NewTaskAPI(repository FailedTaskReadRepository, control *workflow.TaskControlWorkflow) (*TaskAPI, error) {
	if repository == nil || control == nil {
		return nil, errors.New("task API requires failure repository and control workflow")
	}
	return &TaskAPI{repository: repository, control: control}, nil
}

func (api *TaskAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tasks/failed", api.listFailed)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/retry", api.retry)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/dismiss", api.dismiss)
	mux.Handle("/", next)
	return mux
}

func (api *TaskAPI) listFailed(response http.ResponseWriter, request *http.Request) {
	limit := 50
	if value := strings.TrimSpace(request.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeProblem(response, http.StatusBadRequest, "task limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	items, err := api.repository.ListFailedTasks(request.Context(), limit)
	if err != nil {
		writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, struct {
		Items []storage.FailedTaskSummary `json:"items"`
	}{Items: items})
}

func (api *TaskAPI) retry(response http.ResponseWriter, request *http.Request) {
	api.controlTask(response, request, api.control.Retry)
}

func (api *TaskAPI) dismiss(response http.ResponseWriter, request *http.Request) {
	api.controlTask(response, request, api.control.Dismiss)
}

func (api *TaskAPI) controlTask(response http.ResponseWriter, request *http.Request, control func(context.Context, core.TaskID) (core.Task, error)) {
	if !emptyRequestBody(response, request) {
		return
	}
	taskID := core.TaskID(strings.TrimSpace(request.PathValue("task_id")))
	task, err := control(request.Context(), taskID)
	if err != nil {
		writeTaskControlError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, taskControlResult(task))
}

func taskControlResult(task core.Task) TaskControlResult {
	return TaskControlResult{
		ID: task.ID, Type: task.Type, Status: task.Status, ProfileID: task.ProfileID,
		Attempts: task.Attempts, AvailableAt: task.AvailableAt, UpdatedAt: task.UpdatedAt,
		Failure: task.Failure,
	}
}

func writeTaskControlError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, broker.ErrTaskNotFound):
		writeProblem(response, http.StatusNotFound, err.Error())
	case errors.Is(err, broker.ErrTaskNotFailed), errors.Is(err, broker.ErrTaskDeadlineExpired), errors.Is(err, broker.ErrTaskControlConflict):
		writeProblem(response, http.StatusConflict, err.Error())
	default:
		writeError(response, err)
	}
}
