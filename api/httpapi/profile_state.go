package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

type ProfileStateAPI struct {
	planner    *workflow.ProfileStatePlanner
	apply      *workflow.ProfileStateApplyWorkflow
	reconcile  *workflow.ProfileStateReconcileWorkflow
	repository storage.ProfileStateProposalRepository
	revisions  storage.ProfileStateRevisionRepository
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
	EditablePaths  []string       `json:"editable_paths"`
	Reconcilable   bool           `json:"reconcilable"`
}

type ProfileStateEditorField struct {
	Path  string  `json:"path"`
	Kind  string  `json:"kind"`
	Value *string `json:"value"`
}

type ProfileStateResourceEditor struct {
	ResourceTag    string                    `json:"resource_tag"`
	ProfileID      core.ProfileID            `json:"profile_id"`
	ManifestDigest string                    `json:"manifest_digest"`
	Fields         []ProfileStateEditorField `json:"fields"`
}

type profileStatePlanRequest struct {
	BaseManifestDigest string                           `json:"base_manifest_digest"`
	Overrides          []core.ProfileStateValueOverride `json:"overrides"`
}

func NewProfileStateAPI(planner *workflow.ProfileStatePlanner, apply *workflow.ProfileStateApplyWorkflow, reconcile *workflow.ProfileStateReconcileWorkflow, repository storage.ProfileStateProposalRepository, revisions storage.ProfileStateRevisionRepository, readers map[core.ProfileID]adapter.ProfileStateReader) (*ProfileStateAPI, error) {
	if planner == nil || apply == nil || reconcile == nil || repository == nil || revisions == nil {
		return nil, errors.New("profile state API requires planner, apply and reconcile workflows, repository and revisions")
	}
	copiedReaders := make(map[core.ProfileID]adapter.ProfileStateReader, len(readers))
	for profileID, reader := range readers {
		if profileID == "" || reader == nil {
			return nil, errors.New("profile state API readers require profile ids and implementations")
		}
		copiedReaders[profileID] = reader
	}
	return &ProfileStateAPI{planner: planner, apply: apply, reconcile: reconcile, repository: repository, revisions: revisions, readers: copiedReaders}, nil
}

func (api *ProfileStateAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/profile-state/resources", api.listResources)
	mux.HandleFunc("GET /api/v1/profile-state/resources/{resource_tag}/editor", api.getResourceEditor)
	mux.HandleFunc("POST /api/v1/profile-state/resources/{resource_tag}/plans", api.plan)
	mux.HandleFunc("POST /api/v1/profile-state/resources/{resource_tag}/reconcile", api.reconcileResource)
	mux.HandleFunc("POST /api/v1/profile-state/bootstrap/plans", api.planBootstrap)
	mux.HandleFunc("GET /api/v1/profile-state/proposals", api.listProposals)
	mux.HandleFunc("GET /api/v1/profile-state/proposals/{proposal_id}", api.getProposal)
	mux.HandleFunc("GET /api/v1/profile-state/revisions", api.listRevisions)
	mux.HandleFunc("POST /api/v1/profile-state/proposals/{proposal_id}/apply", api.applyProposal)
	mux.HandleFunc("POST /api/v1/profile-state/proposals/{proposal_id}/retry", api.retryProposal)
	mux.HandleFunc("POST /api/v1/profile-state/proposals/{proposal_id}/dismiss", api.dismissProposal)
	mux.Handle("/", next)
	return mux
}

func (api *ProfileStateAPI) retryProposal(response http.ResponseWriter, request *http.Request) {
	api.controlFailedProposal(response, request, api.apply.Retry)
}

func (api *ProfileStateAPI) dismissProposal(response http.ResponseWriter, request *http.Request) {
	api.controlFailedProposal(response, request, api.apply.Dismiss)
}

func (api *ProfileStateAPI) controlFailedProposal(
	response http.ResponseWriter,
	request *http.Request,
	control func(context.Context, core.ProfileStateProposalID) (core.Task, error),
) {
	if !emptyRequestBody(response, request) {
		return
	}
	proposalID := core.ProfileStateProposalID(strings.TrimSpace(request.PathValue("proposal_id")))
	task, err := control(request.Context(), proposalID)
	if err != nil {
		writeProfileStateError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, task)
}

func (api *ProfileStateAPI) planBootstrap(response http.ResponseWriter, request *http.Request) {
	var manifest core.ProfileBootstrapManifest
	if !decodeJSON(response, request, &manifest) {
		return
	}
	if err := manifest.Validate(); err != nil {
		writeProblem(response, http.StatusUnprocessableEntity, err.Error())
		return
	}
	resource, err := manifest.Resource()
	if err != nil {
		writeProblem(response, http.StatusUnprocessableEntity, err.Error())
		return
	}
	reader := api.readers[resource.ProfileID]
	if reader == nil {
		writeProblem(response, http.StatusConflict, "profile bootstrap has no trusted reader")
		return
	}
	if !api.apply.Writable(resource.ProfileID) {
		writeProblem(response, http.StatusConflict, workflow.ErrProfileStateWriterUnavailable.Error())
		return
	}
	proposal, created, err := api.planner.ReadAndPlanResource(request.Context(), resource, reader)
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
			Writable:      api.apply.Writable(resource.ProfileID),
			EditablePaths: editableProfileStatePaths(resource),
			Reconcilable:  readable && api.reconcile.Available(resource.Tag),
		})
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, result)
}

func (api *ProfileStateAPI) reconcileResource(response http.ResponseWriter, request *http.Request) {
	if !emptyRequestBody(response, request) {
		return
	}
	requestKey, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	resourceTag := strings.TrimSpace(request.PathValue("resource_tag"))
	resource, exists := api.planner.Resource(resourceTag)
	if !exists {
		writeProblem(response, http.StatusNotFound, "profile state resource not found")
		return
	}
	if api.readers[resource.ProfileID] == nil || !api.reconcile.Available(resource.Tag) {
		writeProblem(response, http.StatusConflict, workflow.ErrProfileStateReconcileUnavailable.Error())
		return
	}
	task, created, err := api.reconcile.Enqueue(request.Context(), resource.Tag, "api", requestKey)
	if err != nil {
		writeProfileStateError(response, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, status, task)
}

func (api *ProfileStateAPI) getResourceEditor(response http.ResponseWriter, request *http.Request) {
	resourceTag := strings.TrimSpace(request.PathValue("resource_tag"))
	resource, exists := api.planner.Resource(resourceTag)
	if !exists {
		writeProblem(response, http.StatusNotFound, "profile state resource not found")
		return
	}
	paths := editableProfileStatePaths(resource)
	fields := make([]ProfileStateEditorField, 0, len(paths))
	for _, path := range paths {
		raw, exists, err := resource.ValueAt(path)
		if err != nil || !exists {
			writeProblem(response, http.StatusInternalServerError, "load profile state editor")
			return
		}
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil {
			writeProblem(response, http.StatusUnprocessableEntity, "editable profile state value must be text or null")
			return
		}
		fields = append(fields, ProfileStateEditorField{Path: path, Kind: "multiline_text", Value: value})
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, ProfileStateResourceEditor{
		ResourceTag: resource.Tag, ProfileID: resource.ProfileID,
		ManifestDigest: resource.ManifestDigest, Fields: fields,
	})
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
	var body *profileStatePlanRequest
	if request.Body != nil && request.Body != http.NoBody {
		var decoded profileStatePlanRequest
		if !decodeJSON(response, request, &decoded) {
			return
		}
		body = &decoded
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
	var proposal core.ProfileStateProposal
	var created bool
	var err error
	if body == nil {
		proposal, created, err = api.planner.ReadAndPlan(request.Context(), resource.Tag, reader)
	} else {
		if strings.TrimSpace(body.BaseManifestDigest) == "" || len(body.Overrides) == 0 {
			writeProblem(response, http.StatusBadRequest, "profile state override requires base_manifest_digest and overrides")
			return
		}
		if body.BaseManifestDigest != resource.ManifestDigest {
			writeProblem(response, http.StatusConflict, "profile state resource changed; reload the editor")
			return
		}
		editable := make(map[string]struct{})
		for _, path := range editableProfileStatePaths(resource) {
			editable[path] = struct{}{}
		}
		for _, override := range body.Overrides {
			if _, allowed := editable[override.Path]; !allowed {
				writeProblem(response, http.StatusUnprocessableEntity, "profile state path is not editable")
				return
			}
			var textValue *string
			if err := json.Unmarshal(override.Value, &textValue); err != nil {
				writeProblem(response, http.StatusUnprocessableEntity, "editable profile state value must be text or null")
				return
			}
		}
		proposal, created, err = api.planner.ReadAndPlanWithOverrides(request.Context(), resource.Tag, body.Overrides, reader)
	}
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

// editableProfileStatePaths returns declared resume-scoped leaves whose
// desired value is text or null. Objects, arrays, numbers and booleans stay
// outside the text editor; the adapter writer still validates its own
// allowlist and confirms every field with read-back.
func editableProfileStatePaths(resource core.ProfileStateResource) []string {
	paths, err := resource.DeclaredPaths()
	if err != nil {
		return nil
	}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		segments := strings.Split(path, "/")
		if len(segments) < 4 || segments[1] != "resumes" || segments[2] == "" {
			continue
		}
		raw, exists, err := resource.ValueAt(path)
		if err != nil || !exists {
			continue
		}
		var text *string
		if err := json.Unmarshal(raw, &text); err != nil {
			continue
		}
		result = append(result, path)
	}
	return result
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

const (
	profileStateRevisionDefaultLimit = 50
	profileStateRevisionMaximumLimit = 200
)

func (api *ProfileStateAPI) listRevisions(response http.ResponseWriter, request *http.Request) {
	filter := storage.ProfileStateRevisionFilter{
		ResourceTag: strings.TrimSpace(request.URL.Query().Get("resource_tag")),
		ProfileID:   core.ProfileID(strings.TrimSpace(request.URL.Query().Get("profile_id"))),
		Limit:       profileStateRevisionDefaultLimit,
	}
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > profileStateRevisionMaximumLimit {
			writeProblem(response, http.StatusBadRequest, "profile state revision limit must be between 1 and 200")
			return
		}
		filter.Limit = limit
	}
	revisions, err := api.revisions.ListProfileStateRevisions(request.Context(), filter)
	if err != nil {
		writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, listResponse[core.ProfileStateRevision]{Items: revisions})
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
		writeProblem(response, http.StatusBadRequest, "command request body must be empty")
		return false
	}
	return true
}

func writeProfileStateError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, broker.ErrTaskNotFailed), errors.Is(err, broker.ErrTaskDeadlineExpired), errors.Is(err, broker.ErrTaskControlConflict):
		writeProblem(response, http.StatusConflict, err.Error())
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
