package httpapi

import (
	"errors"
	"net/http"

	"github.com/Darkon13/job-agent/core"
)

// ResumeAPI exposes the configured resume targets of a profile. It reads the
// config-derived catalog and never contacts the platform.
type ResumeAPI struct {
	targets map[core.ProfileID][]core.ResumeTarget
}

func NewResumeAPI(targets map[core.ProfileID][]core.ResumeTarget) (*ResumeAPI, error) {
	if targets == nil {
		return nil, errors.New("resume API requires targets")
	}
	return &ResumeAPI{targets: targets}, nil
}

func (api *ResumeAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/profiles/{profile}/resumes", api.listTargets)
	mux.Handle("/", next)
	return mux
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
