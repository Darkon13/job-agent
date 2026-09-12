package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	controller     AuthController
	eventsInterval time.Duration
	logout         *auth.LogoutService
	logoutTargets  map[core.ProfileID]auth.LogoutTarget
	statePaths     map[core.ProfileID]string
}

func NewAuthAPI(controller AuthController, statePaths map[core.ProfileID]string) (*AuthAPI, error) {
	if controller == nil {
		return nil, errors.New("auth API requires a controller")
	}
	return &AuthAPI{controller: controller, eventsInterval: 500 * time.Millisecond, statePaths: statePaths}, nil
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
	mux.HandleFunc("GET /api/v1/auth/sessions/{session_id}/events", api.events)
	mux.HandleFunc("POST /api/v1/profiles/{profile}/logout", api.logoutProfile)
	mux.Handle("/", next)
	return mux
}

// ConfigureLogout attaches the local secret removal. Without it the logout
// endpoint is not registered.
func (api *AuthAPI) ConfigureLogout(service *auth.LogoutService, targets map[core.ProfileID]auth.LogoutTarget) {
	if api == nil || service == nil {
		return
	}
	api.logout = service
	api.logoutTargets = targets
}

func (api *AuthAPI) logoutProfile(response http.ResponseWriter, request *http.Request) {
	if api.logout == nil {
		writeProblem(response, http.StatusNotFound, "logout is not configured")
		return
	}
	profileID := core.ProfileID(request.PathValue("profile"))
	target, exists := api.logoutTargets[profileID]
	if !exists {
		writeProblem(response, http.StatusNotFound, "profile has no local logout target")
		return
	}
	result, err := api.logout.Logout(request.Context(), target)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

// events streams session revisions over SSE until the session reaches a
// terminal status. Dashboard and CLI clients follow the same stream instead of
// polling storage.
func (api *AuthAPI) events(response http.ResponseWriter, request *http.Request) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeProblem(response, http.StatusInternalServerError, "auth events streaming is not supported")
		return
	}
	session, err := api.controller.Session(request.Context(), core.AuthSessionID(request.PathValue("session_id")))
	if err != nil {
		writeError(response, err)
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Connection", "keep-alive")
	writeEvent := func(value core.AuthSession) {
		payload, err := json.Marshal(value)
		if err != nil {
			return
		}
		fmt.Fprintf(response, "event: session\ndata: %s\n\n", payload)
		flusher.Flush()
	}
	writeEvent(session)
	if authSessionTerminal(session.Status) {
		return
	}
	interval := api.eventsInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	revision := session.Revision
	for {
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
			next, err := api.controller.Session(request.Context(), session.ID)
			if err != nil {
				return
			}
			if next.Revision != revision {
				writeEvent(next)
				revision = next.Revision
			}
			if authSessionTerminal(next.Status) {
				return
			}
		}
	}
}

func authSessionTerminal(status core.AuthSessionStatus) bool {
	switch status {
	case core.AuthSessionCompleted, core.AuthSessionExpired, core.AuthSessionCancelled, core.AuthSessionFailed:
		return true
	default:
		return false
	}
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
	// A browser login is stored as the profile's configured storage state
	// when the client does not name an explicit output.
	browserStateReference := strings.TrimSpace(body.BrowserStateReference)
	if browserStateReference == "" && strings.TrimSpace(body.CredentialReference) == "" {
		browserStateReference = strings.TrimSpace(api.statePaths[body.ProfileID])
	}
	session, err := api.controller.Start(request.Context(), auth.StartRequest{
		Platform: body.Platform, ProfileID: body.ProfileID,
		CredentialReference: body.CredentialReference, BrowserStateReference: browserStateReference,
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
