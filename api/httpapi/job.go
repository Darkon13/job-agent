package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Darkon13/job-agent/workflow"
)

type JobAPI struct {
	workflow *workflow.JobRunWorkflow
}

func NewJobAPI(jobWorkflow *workflow.JobRunWorkflow) (*JobAPI, error) {
	if jobWorkflow == nil {
		return nil, errors.New("job API requires workflow")
	}
	return &JobAPI{workflow: jobWorkflow}, nil
}

func (api *JobAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/jobs", api.list)
	mux.HandleFunc("POST /api/v1/jobs/{job_tag}/runs", api.run)
	mux.Handle("/", next)
	return mux
}

func (api *JobAPI) list(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, listResponse[workflow.JobRunDescriptor]{Items: api.workflow.Definitions()})
}

func (api *JobAPI) run(response http.ResponseWriter, request *http.Request) {
	key, ok := requireIdempotencyKey(response, request)
	if !ok || !emptyRequestBody(response, request) {
		return
	}
	task, created, err := api.workflow.Run(request.Context(), strings.TrimSpace(request.PathValue("job_tag")), key)
	if errors.Is(err, workflow.ErrJobRunNotFound) {
		writeProblem(response, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusAccepted, taskResponse{TaskID: task.ID, Created: created})
}
