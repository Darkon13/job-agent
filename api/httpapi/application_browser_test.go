package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/browsercheck"
	"github.com/Darkon13/job-agent/core"
)

type fakeBrowserCheck struct {
	session    browsercheck.Session
	image      []byte
	answer     string
	cancelled  string
	startError error
	answerErr  error
}

func (fake *fakeBrowserCheck) Start(context.Context, core.ApplicationID) (browsercheck.Session, error) {
	if fake.startError != nil {
		return browsercheck.Session{}, fake.startError
	}
	return fake.session, nil
}

func (fake *fakeBrowserCheck) Status(core.ApplicationID, string) (browsercheck.Session, error) {
	return fake.session, nil
}

func (fake *fakeBrowserCheck) Image(context.Context, core.ApplicationID, string) ([]byte, error) {
	return fake.image, nil
}

func (fake *fakeBrowserCheck) Answer(_ context.Context, _ core.ApplicationID, _ string, value string) (browsercheck.Session, error) {
	if fake.answerErr != nil {
		return browsercheck.Session{}, fake.answerErr
	}
	fake.answer = value
	return fake.session, nil
}

func (fake *fakeBrowserCheck) Cancel(_ core.ApplicationID, sessionID string) error {
	fake.cancelled = sessionID
	return nil
}

func browserCheckTestSession() browsercheck.Session {
	return browsercheck.Session{
		ID: "browsercheck-1", ApplicationID: "application-1", ProfileID: "primary",
		State: browsercheck.StateWaitingCaptcha, Message: "введите символы", HasImage: true,
		UpdatedAt: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
	}
}

func TestApplicationBrowserCheckRoutes(t *testing.T) {
	controller := &fakeBrowserCheck{session: browserCheckTestSession(), image: []byte("png")}
	api := &ApplicationAPI{browserCheck: controller}
	handler := api.Handler(nil)

	response := performRequest(t, handler, http.MethodPost, "/api/v1/applications/application-1/browser-check", "", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}
	var started browserCheckResponse
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if started.SessionID != "browsercheck-1" || started.State != "waiting_captcha" || !started.HasImage {
		t.Fatalf("start=%#v", started)
	}

	response = performRequest(t, handler, http.MethodGet, "/api/v1/applications/application-1/browser-check/browsercheck-1", "", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}

	response = performRequest(t, handler, http.MethodGet, "/api/v1/applications/application-1/browser-check/browsercheck-1/image", "", "", nil)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || response.Body.String() != "png" {
		t.Fatalf("image status=%d type=%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}

	response = performRequest(t, handler, http.MethodPost, "/api/v1/applications/application-1/browser-check/browsercheck-1/answer", "", "", browserCheckAnswerRequest{Value: "42"})
	if response.Code != http.StatusOK || controller.answer != "42" {
		t.Fatalf("answer status=%d value=%q", response.Code, controller.answer)
	}

	response = performRequest(t, handler, http.MethodDelete, "/api/v1/applications/application-1/browser-check/browsercheck-1", "", "", nil)
	if response.Code != http.StatusNoContent || controller.cancelled != "browsercheck-1" {
		t.Fatalf("cancel status=%d id=%q", response.Code, controller.cancelled)
	}
}

func TestApplicationBrowserCheckMissingSessionIsNotFound(t *testing.T) {
	controller := &fakeBrowserCheck{startError: browsercheck.ErrSessionNotFound}
	api := &ApplicationAPI{browserCheck: controller}
	handler := api.Handler(nil)
	response := performRequest(t, handler, http.MethodPost, "/api/v1/applications/application-1/browser-check", "", "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestApplicationBrowserCheckUnavailableWithoutController(t *testing.T) {
	api := &ApplicationAPI{}
	handler := api.Handler(nil)
	response := performRequest(t, handler, http.MethodPost, "/api/v1/applications/application-1/browser-check", "", "", nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestApplicationBrowserCheckEmptyImageIsNotFound(t *testing.T) {
	controller := &fakeBrowserCheck{session: browserCheckTestSession()}
	api := &ApplicationAPI{browserCheck: controller}
	handler := api.Handler(nil)
	response := performRequest(t, handler, http.MethodGet, "/api/v1/applications/application-1/browser-check/browsercheck-1/image", "", "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestApplicationBrowserCheckInvalidAnswer(t *testing.T) {
	controller := &fakeBrowserCheck{session: browserCheckTestSession(), answerErr: browsercheck.ErrSessionState}
	api := &ApplicationAPI{browserCheck: controller}
	handler := api.Handler(nil)
	response := performRequest(t, handler, http.MethodPost, "/api/v1/applications/application-1/browser-check/browsercheck-1/answer", "", "", browserCheckAnswerRequest{Value: ""})
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestApplicationBrowserCheckRejectsLargeAnswer(t *testing.T) {
	controller := &fakeBrowserCheck{session: browserCheckTestSession()}
	api := &ApplicationAPI{browserCheck: controller}
	handler := api.Handler(nil)
	response := performRequest(t, handler, http.MethodPost, "/api/v1/applications/application-1/browser-check/browsercheck-1/answer", "", "", browserCheckAnswerRequest{Value: string(make([]byte, maximumBrowserCheckBody+1))})
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestApplicationBrowserCheckUnknownErrorIsBadRequest(t *testing.T) {
	controller := &fakeBrowserCheck{startError: errors.New("driver exploded")}
	api := &ApplicationAPI{browserCheck: controller}
	handler := api.Handler(nil)
	response := performRequest(t, handler, http.MethodPost, "/api/v1/applications/application-1/browser-check", "", "", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
