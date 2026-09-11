package httpapi

import (
	"errors"
	"net/http"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

// QualificationAPI exposes the observed skill verification catalog. Sync only
// reads the platform; starting a limited attempt is a separate explicit step.
type QualificationAPI struct {
	catalog   storage.QualificationCatalogRepository
	workflow  *workflow.QualificationWorkflow
	platforms map[core.ProfileID]core.Platform
}

func NewQualificationAPI(catalog storage.QualificationCatalogRepository, workflow *workflow.QualificationWorkflow, platforms map[core.ProfileID]core.Platform) (*QualificationAPI, error) {
	if catalog == nil || workflow == nil {
		return nil, errors.New("qualification API requires catalog and workflow")
	}
	return &QualificationAPI{catalog: catalog, workflow: workflow, platforms: platforms}, nil
}

func (api *QualificationAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/profiles/{profile}/qualifications", api.list)
	mux.HandleFunc("POST /api/v1/profiles/{profile}/qualifications/sync", api.sync)
	mux.Handle("/", next)
	return mux
}

func (api *QualificationAPI) list(response http.ResponseWriter, request *http.Request) {
	profileID := core.ProfileID(request.PathValue("profile"))
	platform, exists := api.platforms[profileID]
	if !exists {
		writeProblem(response, http.StatusNotFound, "profile has no qualification catalog")
		return
	}
	offerings, err := api.catalog.QualificationOfferings(request.Context(), platform, profileID)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, listResponse[core.QualificationOffering]{Items: offerings})
}

func (api *QualificationAPI) sync(response http.ResponseWriter, request *http.Request) {
	key, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	task, created, err := api.workflow.EnqueueSync(request.Context(), core.ProfileID(request.PathValue("profile")), key)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, taskResponse{TaskID: task.ID, Created: created})
}
