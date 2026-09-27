package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/scheduler"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

// ScheduleReader lists the enabled schedules persisted by the scheduler so the
// jobs endpoint can show the next enqueue time. It is optional: without it the
// API still lists runnable jobs.
type ScheduleReader interface {
	Schedules(ctx context.Context) ([]scheduler.Entry, error)
}

// JobPauseRepository stores paused (job, profile) pairs for the dashboard and
// CLI. Without it the pause endpoints answer 503.
type JobPauseRepository interface {
	PauseJob(ctx context.Context, jobTag string, profileID core.ProfileID, reason string, now time.Time) error
	PauseJobs(ctx context.Context, jobTag string, reason string, now time.Time) (int, error)
	ResumeJob(ctx context.Context, jobTag string, profileID core.ProfileID) (int, error)
	ListJobPauses(ctx context.Context) ([]storage.JobPause, error)
	// TriggerScheduledJob runs the resumed job at the next reconcile.
	TriggerScheduledJob(ctx context.Context, jobTag string, profileID core.ProfileID, now time.Time) (int, error)
}

type JobAPI struct {
	workflow     *workflow.JobRunWorkflow
	schedules    ScheduleReader
	descriptions map[string]string
	systemTags   map[string]struct{}
	pauses       JobPauseRepository
}

// SetSystemTags marks the generated jobs (state harvest, session refresh and
// so on). The dashboard shows them in their own group.
func (api *JobAPI) SetSystemTags(tags []string) {
	api.systemTags = make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		api.systemTags[tag] = struct{}{}
	}
}

// isSystem reports whether the tag belongs to a generated system job.
func (api *JobAPI) isSystem(tag string) bool {
	_, exists := api.systemTags[tag]
	return exists
}

// SetPauses attaches the pause storage so jobs can be paused and resumed.
func (api *JobAPI) SetPauses(repository JobPauseRepository) {
	api.pauses = repository
}

// jobListView is a runnable job plus its pause state and group.
type jobListView struct {
	workflow.JobRunDescriptor
	System bool               `json:"system"`
	Paused bool               `json:"paused"`
	Pauses []storage.JobPause `json:"pauses,omitempty"`
}

// SetDescriptions attaches optional per-job descriptions from the config so the
// dashboard can show the operator's own wording instead of the task type.
func (api *JobAPI) SetDescriptions(descriptions map[string]string) {
	api.descriptions = descriptions
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
	mux.HandleFunc("POST /api/v1/jobs/{job_tag}/pause", api.pause)
	mux.HandleFunc("POST /api/v1/jobs/{job_tag}/resume", api.resume)
	mux.HandleFunc("POST /api/v1/jobs/pause", api.pauseGroup)
	mux.HandleFunc("POST /api/v1/jobs/resume", api.resumeGroup)
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
			if entry.Interval > 0 {
				schedule.Interval = entry.Interval.String()
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
	for index := range items {
		if description := strings.TrimSpace(api.descriptions[items[index].Tag]); description != "" {
			items[index].Description = description
		}
	}
	pauses, err := api.listPauses(request.Context())
	if err != nil {
		writeError(response, err)
		return
	}
	notes := make(map[string][]storage.JobPause, len(pauses))
	for _, pause := range pauses {
		notes[pause.JobTag] = append(notes[pause.JobTag], pause)
	}
	views := make([]jobListView, 0, len(items))
	for _, item := range items {
		views = append(views, jobListView{
			JobRunDescriptor: item, System: api.isSystem(item.Tag),
			Paused: len(notes[item.Tag]) > 0, Pauses: notes[item.Tag],
		})
	}
	writeJSON(response, http.StatusOK, listResponse[jobListView]{Items: views})
}

// listPauses returns the stored pauses, or nothing when pausing is not wired.
func (api *JobAPI) listPauses(ctx context.Context) ([]storage.JobPause, error) {
	if api.pauses == nil {
		return nil, nil
	}
	return api.pauses.ListJobPauses(ctx)
}

func (api *JobAPI) pause(response http.ResponseWriter, request *http.Request) {
	api.setPaused(response, request, true)
}

func (api *JobAPI) resume(response http.ResponseWriter, request *http.Request) {
	api.setPaused(response, request, false)
}

// setPaused pauses or resumes one job: a single profile when profile_id is
// given, otherwise every profile the job covers. A paused schedule stops
// creating tasks and runs immediately after resume.
func (api *JobAPI) setPaused(response http.ResponseWriter, request *http.Request, paused bool) {
	if api.pauses == nil {
		writeProblem(response, http.StatusServiceUnavailable, "job pauses are not configured")
		return
	}
	tag := strings.TrimSpace(request.PathValue("job_tag"))
	if !api.hasJob(tag) {
		writeProblem(response, http.StatusNotFound, "job not found")
		return
	}
	if paused && api.isSystem(tag) {
		writeProblem(response, http.StatusConflict, "system jobs are paused by the service itself, not by the operator")
		return
	}
	profileID := core.ProfileID(strings.TrimSpace(request.URL.Query().Get("profile_id")))
	now := time.Now().UTC()
	affected := 0
	var err error
	if paused {
		if profileID == "" {
			affected, err = api.pauses.PauseJobs(request.Context(), tag, "operator", now)
		} else {
			err = api.pauses.PauseJob(request.Context(), tag, profileID, "operator", now)
			if err == nil {
				affected = 1
			}
		}
	} else {
		affected, err = api.pauses.ResumeJob(request.Context(), tag, profileID)
		if err == nil {
			// A resumed job runs right away instead of waiting for its next
			// natural occurrence.
			if _, triggerErr := api.pauses.TriggerScheduledJob(request.Context(), tag, profileID, now); triggerErr != nil {
				err = triggerErr
			}
		}
	}
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Tag       string         `json:"tag"`
		ProfileID core.ProfileID `json:"profile_id,omitempty"`
		Paused    bool           `json:"paused"`
		Affected  int            `json:"affected"`
	}{Tag: tag, ProfileID: profileID, Paused: paused, Affected: affected})
}

// pauseGroup pauses every job of a group: "system" for the generated jobs,
// "user" for the ones declared in the configuration.
func (api *JobAPI) pauseGroup(response http.ResponseWriter, request *http.Request) {
	api.setGroupPaused(response, request, true)
}

func (api *JobAPI) resumeGroup(response http.ResponseWriter, request *http.Request) {
	api.setGroupPaused(response, request, false)
}

func (api *JobAPI) setGroupPaused(response http.ResponseWriter, request *http.Request, paused bool) {
	if api.pauses == nil {
		writeProblem(response, http.StatusServiceUnavailable, "job pauses are not configured")
		return
	}
	group := strings.TrimSpace(request.URL.Query().Get("group"))
	if group != "system" && group != "user" {
		writeProblem(response, http.StatusBadRequest, "job group must be system or user")
		return
	}
	if paused && group == "system" {
		// The service pauses system jobs itself (for example on logout); the
		// operator only needs the recovery path.
		writeProblem(response, http.StatusConflict, "system jobs are paused by the service itself, not by the operator")
		return
	}
	now := time.Now().UTC()
	affected := 0
	for _, definition := range api.workflow.Definitions() {
		if (group == "system") != api.isSystem(definition.Tag) {
			continue
		}
		if paused {
			count, err := api.pauses.PauseJobs(request.Context(), definition.Tag, "operator", now)
			if err != nil {
				writeError(response, err)
				return
			}
			affected += count
			continue
		}
		count, err := api.pauses.ResumeJob(request.Context(), definition.Tag, "")
		if err != nil {
			writeError(response, err)
			return
		}
		if _, triggerErr := api.pauses.TriggerScheduledJob(request.Context(), definition.Tag, "", now); triggerErr != nil {
			writeError(response, triggerErr)
			return
		}
		affected += count
	}
	writeJSON(response, http.StatusOK, struct {
		Group    string `json:"group"`
		Paused   bool   `json:"paused"`
		Affected int    `json:"affected"`
	}{Group: group, Paused: paused, Affected: affected})
}

// hasJob reports whether the tag belongs to a runnable job.
func (api *JobAPI) hasJob(tag string) bool {
	if tag == "" {
		return false
	}
	for _, definition := range api.workflow.Definitions() {
		if definition.Tag == tag {
			return true
		}
	}
	return false
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
