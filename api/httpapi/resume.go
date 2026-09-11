package httpapi

import (
	"errors"
	"net/http"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/workflow"
)

// ResumeAPI exposes the configured resume targets of a profile. It reads the
// config-derived catalog and never contacts the platform.
type ResumeAPI struct {
	targets  map[core.ProfileID][]core.ResumeTarget
	workflow *workflow.ResumeUpdateWorkflow
}

func NewResumeAPI(targets map[core.ProfileID][]core.ResumeTarget) (*ResumeAPI, error) {
	if targets == nil {
		return nil, errors.New("resume API requires targets")
	}
	return &ResumeAPI{targets: targets}, nil
}

// ConfigureUpdate attaches the durable resume update pipeline.
func (api *ResumeAPI) ConfigureUpdate(workflow *workflow.ResumeUpdateWorkflow) {
	if api == nil {
		return
	}
	api.workflow = workflow
}

func (api *ResumeAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/profiles/{profile}/resumes", api.listTargets)
	mux.HandleFunc("POST /api/v1/profiles/{profile}/resumes/{resume}/update", api.updateResume)
	mux.Handle("/", next)
	return mux
}

type resumeUpdateRequest struct {
	ResourceTag string `json:"resource_tag"`
	Publish     bool   `json:"publish,omitempty"`
}

func (api *ResumeAPI) updateResume(response http.ResponseWriter, request *http.Request) {
	if api.workflow == nil {
		writeProblem(response, http.StatusNotFound, "resume update is not configured")
		return
	}
	key, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	var body resumeUpdateRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	task, created, err := api.workflow.EnqueueUpdate(request.Context(),
		core.ProfileID(request.PathValue("profile")), body.ResourceTag,
		request.PathValue("resume"), body.Publish, key)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, taskResponse{TaskID: task.ID, Created: created})
}

func (api *ResumeAPI) listTargets(response http.ResponseWriter, request *http.Request) {
	profileID := core.ProfileID(request.PathValue("profile"))
	targets, exists := api.targets[profileID]
	if !exists {
		writeProblem(response, http.StatusNotFound, "profile has no registered resume targets")
		return
	}
	writeJSON(response, http.StatusOK, listResponse[core.ResumeTarget]{Items: targets})
}
