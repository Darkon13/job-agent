package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/auth"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// AuthController is the subset of the auth control plane the local API needs.
type AuthController interface {
	Start(context.Context, auth.StartRequest) (core.AuthSession, error)
	Submit(context.Context, core.AuthSessionID, auth.Input) (core.AuthSession, error)
	Cancel(context.Context, core.AuthSessionID) (core.AuthSession, error)
	Session(context.Context, core.AuthSessionID) (core.AuthSession, error)
	ChallengePayload(context.Context, core.AuthSessionID) (auth.ChallengePayload, error)
}

type AuthAPI struct {
	controller AuthController
}

func NewAuthAPI(controller AuthController) (*AuthAPI, error) {
	if controller == nil {
		return nil, errors.New("auth API requires a controller")
	}
	return &AuthAPI{controller: controller}, nil
}

func (api *AuthAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/sessions", api.start)
	mux.HandleFunc("GET /api/v1/auth/sessions/{session_id}", api.read)
	mux.HandleFunc("POST /api/v1/auth/sessions/{session_id}/inputs", api.submit)
	mux.HandleFunc("POST /api/v1/auth/sessions/{session_id}/cancel", api.cancel)
	mux.HandleFunc("GET /api/v1/auth/sessions/{session_id}/challenge", api.challenge)
	mux.Handle("/", next)
	return mux
}

type authSessionRequest struct {
	Platform              core.Platform  `json:"platform"`
	ProfileID             core.ProfileID `json:"profile_id"`
	CredentialReference   string         `json:"credential_reference,omitempty"`
	BrowserStateReference string         `json:"browser_state_reference,omitempty"`
	TTL                   string         `json:"ttl,omitempty"`
}

type authInputRequest struct {
	Kind  auth.InputKind `json:"kind"`
	Value string         `json:"value"`
}

func (api *AuthAPI) start(response http.ResponseWriter, request *http.Request) {
	var body authSessionRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	var ttl time.Duration
	if strings.TrimSpace(body.TTL) != "" {
		parsed, err := time.ParseDuration(body.TTL)
		if err != nil {
			writeProblem(response, http.StatusBadRequest, "auth session ttl must be a Go duration")
			return
		}
		ttl = parsed
	}
	session, err := api.controller.Start(request.Context(), auth.StartRequest{
		Platform: body.Platform, ProfileID: body.ProfileID,
		CredentialReference: body.CredentialReference, BrowserStateReference: body.BrowserStateReference,
		TTL: ttl,
	})
	if err != nil {
		writeAuthError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusCreated, session)
}

func (api *AuthAPI) read(response http.ResponseWriter, request *http.Request) {
	session, err := api.controller.Session(request.Context(), authSessionID(request))
	if err != nil {
		writeAuthError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, session)
}

func (api *AuthAPI) submit(response http.ResponseWriter, request *http.Request) {
	var body authInputRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	session, err := api.controller.Submit(request.Context(), authSessionID(request), auth.Input{Kind: body.Kind, Value: body.Value})
	if err != nil {
		writeAuthError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, session)
}

func (api *AuthAPI) cancel(response http.ResponseWriter, request *http.Request) {
	if !emptyRequestBody(response, request) {
		return
	}
	session, err := api.controller.Cancel(request.Context(), authSessionID(request))
	if err != nil {
		writeAuthError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, session)
}

func (api *AuthAPI) challenge(response http.ResponseWriter, request *http.Request) {
	payload, err := api.controller.ChallengePayload(request.Context(), authSessionID(request))
	if err != nil {
		writeAuthError(response, err)
		return
	}
	mediaType := strings.TrimSpace(payload.MediaType)
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	response.Header().Set("Content-Type", mediaType)
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(payload.Data)
}

func authSessionID(request *http.Request) core.AuthSessionID {
	return core.AuthSessionID(strings.TrimSpace(request.PathValue("session_id")))
}

func writeAuthError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrAuthSessionNotFound), errors.Is(err, auth.ErrChallengeNotFound):
		writeProblem(response, http.StatusNotFound, err.Error())
	case errors.Is(err, auth.ErrAuthSessionSettled), errors.Is(err, auth.ErrAuthInputMismatch),
		errors.Is(err, storage.ErrRevisionConflict):
		writeProblem(response, http.StatusConflict, err.Error())
	default:
		writeError(response, err)
	}
}
