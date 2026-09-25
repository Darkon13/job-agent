package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/browsercheck"
	"github.com/Darkon13/job-agent/core"
)

const maximumBrowserCheckBody = 8 << 10

// browserCheckController is the operator-facing surface of interactive
// browser checks: start, poll, read the captcha image, answer, cancel.
type browserCheckController interface {
	Start(ctx context.Context, applicationID core.ApplicationID) (browsercheck.Session, error)
	Status(applicationID core.ApplicationID, sessionID string) (browsercheck.Session, error)
	Image(ctx context.Context, applicationID core.ApplicationID, sessionID string) ([]byte, error)
	Answer(ctx context.Context, applicationID core.ApplicationID, sessionID string, value string) (browsercheck.Session, error)
	Cancel(applicationID core.ApplicationID, sessionID string) error
}

type browserCheckResponse struct {
	SessionID     string    `json:"session_id"`
	ApplicationID string    `json:"application_id"`
	ProfileID     string    `json:"profile_id,omitempty"`
	State         string    `json:"state"`
	Message       string    `json:"message,omitempty"`
	HasImage      bool      `json:"has_image"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type browserCheckAnswerRequest struct {
	Value string `json:"value"`
}

func newBrowserCheckResponse(session browsercheck.Session) browserCheckResponse {
	return browserCheckResponse{
		SessionID:     session.ID,
		ApplicationID: string(session.ApplicationID),
		ProfileID:     string(session.ProfileID),
		State:         string(session.State),
		Message:       session.Message,
		HasImage:      session.HasImage,
		UpdatedAt:     session.UpdatedAt,
	}
}

func (api *ApplicationAPI) browserCheckConfiguration(response http.ResponseWriter) bool {
	if api.browserCheck == nil {
		writeProblem(response, http.StatusServiceUnavailable, "browser check is not configured")
		return false
	}
	return true
}

// startBrowserCheck launches the browser flow of one blocked application.
func (api *ApplicationAPI) startBrowserCheck(response http.ResponseWriter, request *http.Request) {
	if !api.browserCheckConfiguration(response) {
		return
	}
	applicationID := core.ApplicationID(strings.TrimSpace(request.PathValue("application_id")))
	session, err := api.browserCheck.Start(request.Context(), applicationID)
	if err != nil {
		writeBrowserCheckError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, newBrowserCheckResponse(session))
}

// browserCheckStatus returns the stored session without touching the browser.
func (api *ApplicationAPI) browserCheckStatus(response http.ResponseWriter, request *http.Request) {
	if !api.browserCheckConfiguration(response) {
		return
	}
	session, err := api.browserCheck.Status(applicationIDFromPath(request), request.PathValue("session_id"))
	if err != nil {
		writeBrowserCheckError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, newBrowserCheckResponse(session))
}

// browserCheckImage serves the captcha (or review) screenshot of one session.
func (api *ApplicationAPI) browserCheckImage(response http.ResponseWriter, request *http.Request) {
	if !api.browserCheckConfiguration(response) {
		return
	}
	image, err := api.browserCheck.Image(request.Context(), applicationIDFromPath(request), request.PathValue("session_id"))
	if err != nil {
		writeBrowserCheckError(response, err)
		return
	}
	if len(image) == 0 {
		writeProblem(response, http.StatusNotFound, "browser check session has no image")
		return
	}
	response.Header().Set("Content-Type", "image/png")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(image)
}

// answerBrowserCheck delivers the operator's captcha answer.
func (api *ApplicationAPI) answerBrowserCheck(response http.ResponseWriter, request *http.Request) {
	if !api.browserCheckConfiguration(response) {
		return
	}
	var payload browserCheckAnswerRequest
	if err := decodeLimitedJSON(response, request, &payload); err != nil {
		return
	}
	session, err := api.browserCheck.Answer(request.Context(), applicationIDFromPath(request), request.PathValue("session_id"), payload.Value)
	if err != nil {
		writeBrowserCheckError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, newBrowserCheckResponse(session))
}

// cancelBrowserCheck forgets a session without touching the platform.
func (api *ApplicationAPI) cancelBrowserCheck(response http.ResponseWriter, request *http.Request) {
	if !api.browserCheckConfiguration(response) {
		return
	}
	if err := api.browserCheck.Cancel(applicationIDFromPath(request), request.PathValue("session_id")); err != nil {
		writeBrowserCheckError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusNoContent)
}

// writeBrowserCheckError maps session errors to their HTTP meaning.
func writeBrowserCheckError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, browsercheck.ErrSessionNotFound):
		writeProblem(response, http.StatusNotFound, err.Error())
	case errors.Is(err, browsercheck.ErrSessionBusy), errors.Is(err, browsercheck.ErrSessionState):
		writeProblem(response, http.StatusConflict, err.Error())
	default:
		writeError(response, err)
	}
}

func applicationIDFromPath(request *http.Request) core.ApplicationID {
	return core.ApplicationID(strings.TrimSpace(request.PathValue("application_id")))
}

// decodeLimitedJSON reads one small JSON body and reports a problem on failure.
func decodeLimitedJSON(response http.ResponseWriter, request *http.Request, target any) error {
	body, err := io.ReadAll(io.LimitReader(request.Body, maximumBrowserCheckBody+1))
	if err != nil {
		writeProblem(response, http.StatusBadRequest, "read request body")
		return err
	}
	if len(body) > maximumBrowserCheckBody {
		writeProblem(response, http.StatusRequestEntityTooLarge, "request body is too large")
		return errors.New("request body is too large")
	}
	if err := json.Unmarshal(body, target); err != nil {
		writeProblem(response, http.StatusBadRequest, "invalid request body")
		return err
	}
	return nil
}
