package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

type ProfileStateAPI struct {
	planner    *workflow.ProfileStatePlanner
	apply      *workflow.ProfileStateApplyWorkflow
	repository storage.ProfileStateProposalRepository
	readers    map[core.ProfileID]adapter.ProfileStateReader
}

type ProfileStateResourceSummary struct {
	Tag            string         `json:"tag"`
	ProfileID      core.ProfileID `json:"profile_id"`
	Ownership      string         `json:"ownership"`
	ManifestDigest string         `json:"manifest_digest"`
	Paths          []string       `json:"paths"`
	Readable       bool           `json:"readable"`
	Writable       bool           `json:"writable"`
}

func NewProfileStateAPI(planner *workflow.ProfileStatePlanner, apply *workflow.ProfileStateApplyWorkflow, repository storage.ProfileStateProposalRepository, readers map[core.ProfileID]adapter.ProfileStateReader) (*ProfileStateAPI, error) {
	if planner == nil || apply == nil || repository == nil {
		return nil, errors.New("profile state API requires planner, apply workflow and repository")
	}
	copiedReaders := make(map[core.ProfileID]adapter.ProfileStateReader, len(readers))
	for profileID, reader := range readers {
		if profileID == "" || reader == nil {
			return nil, errors.New("profile state API readers require profile ids and implementations")
		}
		copiedReaders[profileID] = reader
	}
	return &ProfileStateAPI{planner: planner, apply: apply, repository: repository, readers: copiedReaders}, nil
}

func (api *ProfileStateAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/profile-state/resources", api.listResources)
	mux.HandleFunc("POST /api/v1/profile-state/resources/{resource_tag}/plans", api.plan)
	mux.HandleFunc("GET /api/v1/profile-state/proposals", api.listProposals)
	mux.HandleFunc("GET /api/v1/profile-state/proposals/{proposal_id}", api.getProposal)
	mux.HandleFunc("POST /api/v1/profile-state/proposals/{proposal_id}/apply", api.applyProposal)
	mux.Handle("/", next)
	return mux
}

func (api *ProfileStateAPI) listResources(response http.ResponseWriter, _ *http.Request) {
	resources := api.planner.Resources()
	result := make([]ProfileStateResourceSummary, 0, len(resources))
	for _, resource := range resources {
		paths, err := resource.DeclaredPaths()
		if err != nil {
			writeProblem(response, http.StatusInternalServerError, "load profile state resource")
			return
		}
		_, readable := api.readers[resource.ProfileID]
		result = append(result, ProfileStateResourceSummary{
			Tag: resource.Tag, ProfileID: resource.ProfileID, Ownership: resource.Ownership,
			ManifestDigest: resource.ManifestDigest, Paths: paths, Readable: readable,
			Writable: api.apply.Writable(resource.ProfileID),
		})
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, result)
}

func (api *ProfileStateAPI) applyProposal(response http.ResponseWriter, request *http.Request) {
	if !emptyRequestBody(response, request) {
		return
	}
	proposalID := core.ProfileStateProposalID(strings.TrimSpace(request.PathValue("proposal_id")))
	task, created, err := api.apply.Enqueue(request.Context(), proposalID, "api")
	if err != nil {
		switch {
		case errors.Is(err, workflow.ErrProfileStateNoChanges), errors.Is(err, workflow.ErrProfileStateWriterUnavailable):
			writeProblem(response, http.StatusConflict, err.Error())
		default:
			writeProfileStateError(response, err)
		}
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, status, task)
}

func (api *ProfileStateAPI) plan(response http.ResponseWriter, request *http.Request) {
	if !emptyRequestBody(response, request) {
		return
	}
	resourceTag := strings.TrimSpace(request.PathValue("resource_tag"))
	resource, exists := api.planner.Resource(resourceTag)
	if !exists {
		writeProblem(response, http.StatusNotFound, "profile state resource not found")
		return
	}
	reader := api.readers[resource.ProfileID]
	if reader == nil {
		writeProblem(response, http.StatusConflict, "profile state resource has no trusted reader")
		return
	}
	proposal, created, err := api.planner.ReadAndPlan(request.Context(), resource.Tag, reader)
	if err != nil {
		writeProfileStateError(response, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, status, proposal)
}

func (api *ProfileStateAPI) listProposals(response http.ResponseWriter, request *http.Request) {
	filter := storage.ProfileStateProposalFilter{
		ResourceTag: strings.TrimSpace(request.URL.Query().Get("resource_tag")),
		ProfileID:   core.ProfileID(strings.TrimSpace(request.URL.Query().Get("profile_id"))),
		Status:      core.ProfileStateProposalStatus(strings.TrimSpace(request.URL.Query().Get("status"))),
	}
	if filter.Status != "" && filter.Status != core.ProfileStateProposalPlanned && filter.Status != core.ProfileStateProposalNoChanges {
		writeProblem(response, http.StatusBadRequest, "unsupported profile state proposal status")
		return
	}
	proposals, err := api.repository.ListProfileStateProposals(request.Context(), filter)
	if err != nil {
		writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, proposals)
}

func (api *ProfileStateAPI) getProposal(response http.ResponseWriter, request *http.Request) {
	proposalID := core.ProfileStateProposalID(strings.TrimSpace(request.PathValue("proposal_id")))
	proposal, err := api.repository.ProfileStateProposal(request.Context(), proposalID)
	if err != nil {
		writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, proposal)
}

func emptyRequestBody(response http.ResponseWriter, request *http.Request) bool {
	if request.Body == nil || request.Body == http.NoBody {
		return true
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, 1))
	if err != nil {
		writeProblem(response, http.StatusBadRequest, "read request body")
		return false
	}
	if len(data) != 0 {
		writeProblem(response, http.StatusBadRequest, "profile state command request body must be empty")
		return false
	}
	return true
}

func writeProfileStateError(response http.ResponseWriter, err error) {
	switch {
	case core.ErrorIsCategory(err, core.ErrorUnauthorized):
		writeProblem(response, http.StatusUnauthorized, err.Error())
	case core.ErrorIsCategory(err, core.ErrorUnsupported), core.ErrorIsCategory(err, core.ErrorValidationRequired):
		writeProblem(response, http.StatusUnprocessableEntity, err.Error())
	case core.ErrorIsCategory(err, core.ErrorRateLimited), core.ErrorIsCategory(err, core.ErrorQuotaExceeded):
		writeProblem(response, http.StatusTooManyRequests, err.Error())
	case core.ErrorIsCategory(err, core.ErrorTemporaryFailure):
		writeProblem(response, http.StatusServiceUnavailable, err.Error())
	case core.ErrorIsCategory(err, core.ErrorConflict):
		writeProblem(response, http.StatusConflict, err.Error())
	case core.ErrorIsCategory(err, core.ErrorPermanentFailure):
		writeProblem(response, http.StatusBadGateway, err.Error())
	default:
		writeError(response, err)
	}
}
