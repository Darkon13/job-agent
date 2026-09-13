package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Darkon13/job-agent/scheduler"
	"github.com/Darkon13/job-agent/workflow"
)

// ScheduleReader lists the enabled schedules persisted by the scheduler so the
// jobs endpoint can show the next enqueue time. It is optional: without it the
// API still lists runnable jobs.
type ScheduleReader interface {
	Schedules(ctx context.Context) ([]scheduler.Entry, error)
}

type JobAPI struct {
	workflow  *workflow.JobRunWorkflow
	schedules ScheduleReader
}

func NewJobAPI(jobWorkflow *workflow.JobRunWorkflow, schedules ScheduleReader) (*JobAPI, error) {
	if jobWorkflow == nil {
		return nil, errors.New("job API requires workflow")
	}
	return &JobAPI{workflow: jobWorkflow, schedules: schedules}, nil
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

func (api *JobAPI) list(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	items := api.workflow.Definitions()
	if api.schedules != nil {
		entries, err := api.schedules.Schedules(request.Context())
		if err != nil {
			writeError(response, err)
			return
		}
		byTag := make(map[string][]workflow.JobSchedule)
		for _, entry := range entries {
			schedule := workflow.JobSchedule{
				TriggerIndex: entry.TriggerIndex, Expression: entry.Expression,
				Timezone: entry.Timezone, NextRunAt: entry.NextRunAt,
			}
			if entry.JitterMin > 0 || entry.JitterMax > 0 {
				schedule.JitterMin = entry.JitterMin.String()
				schedule.JitterMax = entry.JitterMax.String()
			}
			byTag[entry.JobTag] = append(byTag[entry.JobTag], schedule)
		}
		for index := range items {
			items[index].Schedules = byTag[items[index].Tag]
		}
	}
	writeJSON(response, http.StatusOK, listResponse[workflow.JobRunDescriptor]{Items: items})
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
