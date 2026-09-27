package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

// ProfileDraftController is the workflow subset the profile onboarding API
// needs.
type ProfileDraftController interface {
	Create(ctx context.Context, tag, adapter string) (core.ProfileDraft, error)
	List(ctx context.Context) ([]core.ProfileDraft, error)
	Get(ctx context.Context, tag string) (core.ProfileDraft, error)
	Delete(ctx context.Context, tag string) error
	CaptureIdentity(ctx context.Context, tag string) (core.ProfileDraft, error)
	Apply(ctx context.Context, tag, primaryResumeID string) (core.ProfileDraft, error)
	Directory() string
}

// ProfileDraftAPI exposes the dashboard-managed profiles before they become
// config fragments. The state file path is always derived by the backend.
type ProfileDraftAPI struct {
	drafts ProfileDraftController
}

func NewProfileDraftAPI(drafts ProfileDraftController) (*ProfileDraftAPI, error) {
	if drafts == nil {
		return nil, errors.New("profile draft API requires a controller")
	}
	return &ProfileDraftAPI{drafts: drafts}, nil
}

func (api *ProfileDraftAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/profile-drafts", api.list)
	mux.HandleFunc("POST /api/v1/profile-drafts", api.create)
	mux.HandleFunc("GET /api/v1/profile-drafts/{tag}", api.read)
	mux.HandleFunc("DELETE /api/v1/profile-drafts/{tag}", api.remove)
	mux.HandleFunc("POST /api/v1/profile-drafts/{tag}/refresh", api.refresh)
	mux.HandleFunc("POST /api/v1/profile-drafts/{tag}/apply", api.apply)
	mux.Handle("/", next)
	return mux
}

type profileDraftListView struct {
	Items     []core.ProfileDraft `json:"items"`
	Directory string              `json:"directory,omitempty"`
}

type profileDraftCreateRequest struct {
	Tag     string `json:"tag"`
	Adapter string `json:"adapter,omitempty"`
}

type profileDraftApplyRequest struct {
	PrimaryResume string `json:"primary_resume,omitempty"`
}

func (api *ProfileDraftAPI) list(response http.ResponseWriter, request *http.Request) {
	items, err := api.drafts.List(request.Context())
	if err != nil {
		writeError(response, err)
		return
	}
	if items == nil {
		items = []core.ProfileDraft{}
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, profileDraftListView{Items: items, Directory: api.drafts.Directory()})
}

func (api *ProfileDraftAPI) create(response http.ResponseWriter, request *http.Request) {
	var body profileDraftCreateRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	draft, err := api.drafts.Create(request.Context(), body.Tag, body.Adapter)
	if err != nil {
		writeProfileDraftError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusCreated, draft)
}

func (api *ProfileDraftAPI) read(response http.ResponseWriter, request *http.Request) {
	draft, err := api.drafts.Get(request.Context(), request.PathValue("tag"))
	if err != nil {
		writeProfileDraftError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, draft)
}

func (api *ProfileDraftAPI) remove(response http.ResponseWriter, request *http.Request) {
	if !emptyRequestBody(response, request) {
		return
	}
	if err := api.drafts.Delete(request.Context(), request.PathValue("tag")); err != nil {
		writeProfileDraftError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (api *ProfileDraftAPI) refresh(response http.ResponseWriter, request *http.Request) {
	if !emptyRequestBody(response, request) {
		return
	}
	draft, err := api.drafts.CaptureIdentity(request.Context(), request.PathValue("tag"))
	if err != nil {
		writeProfileDraftError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, draft)
}

func (api *ProfileDraftAPI) apply(response http.ResponseWriter, request *http.Request) {
	var body profileDraftApplyRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	draft, err := api.drafts.Apply(request.Context(), request.PathValue("tag"), body.PrimaryResume)
	if err != nil {
		writeProfileDraftError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, draft)
}

func writeProfileDraftError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrProfileDraftNotFound):
		writeProblem(response, http.StatusNotFound, err.Error())
	case errors.Is(err, workflow.ErrProfileDraftExists):
		writeProblem(response, http.StatusConflict, err.Error())
	default:
		message := err.Error()
		if strings.Contains(message, "must be a lower-case slug") ||
			strings.Contains(message, "is already declared") ||
			strings.Contains(message, "unknown adapter") ||
			strings.Contains(message, "is not one of the captured resumes") ||
			strings.Contains(message, "has no captured identity") {
			writeProblem(response, http.StatusBadRequest, message)
			return
		}
		writeError(response, err)
	}
}
