package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/auth"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type fakeAuthController struct {
	session    core.AuthSession
	payload    auth.ChallengePayload
	startErr   error
	submitErr  error
	cancelErr  error
	readErr    error
	payloadErr error
	lastStart  auth.StartRequest
	lastInput  auth.Input
	lastID     core.AuthSessionID
}

func (controller *fakeAuthController) Start(_ context.Context, request auth.StartRequest) (core.AuthSession, error) {
	controller.lastStart = request
	return controller.session, controller.startErr
}

func (controller *fakeAuthController) Submit(_ context.Context, id core.AuthSessionID, input auth.Input) (core.AuthSession, error) {
	controller.lastID = id
	controller.lastInput = input
	return controller.session, controller.submitErr
}

func (controller *fakeAuthController) Cancel(_ context.Context, id core.AuthSessionID) (core.AuthSession, error) {
	controller.lastID = id
	return controller.session, controller.cancelErr
}

func (controller *fakeAuthController) Session(_ context.Context, id core.AuthSessionID) (core.AuthSession, error) {
	controller.lastID = id
	return controller.session, controller.readErr
}

func (controller *fakeAuthController) ChallengePayload(_ context.Context, id core.AuthSessionID) (auth.ChallengePayload, error) {
	controller.lastID = id
	return controller.payload, controller.payloadErr
}

func authAPIFixture(t *testing.T) (*fakeAuthController, http.Handler) {
	t.Helper()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	session, err := core.NewAuthSession(core.NewAuthSessionParams{
		ID: "auth-1", Platform: "hh", ProfileID: "primary",
		CredentialReference: "file:/run/secrets/hh-primary.json",
		ExpiresAt:           now.Add(15 * time.Minute),
	}, now)
	if err != nil {
		t.Fatalf("new auth session: %v", err)
	}
	if err := session.BeginIdentifier(now.Add(time.Second)); err != nil {
		t.Fatalf("begin identifier: %v", err)
	}
	controller := &fakeAuthController{session: session}
	api, err := NewAuthAPI(controller, nil)
	if err != nil {
		t.Fatalf("new auth API: %v", err)
	}
	return controller, api.Handler(nil)
}

func TestAuthAPIStartsSession(t *testing.T) {
	controller, handler := authAPIFixture(t)
	response := performRequest(t, handler, http.MethodPost, "/api/v1/auth/sessions", "", "", authSessionRequest{
		Platform: "hh", ProfileID: "primary",
		CredentialReference: "file:/run/secrets/hh-primary.json", TTL: "10m",
	})
	if response.Code != http.StatusCreated || controller.lastStart.Platform != "hh" ||
		controller.lastStart.ProfileID != "primary" || controller.lastStart.TTL != 10*time.Minute {
		t.Fatalf("status=%d start=%#v body=%s", response.Code, controller.lastStart, response.Body.String())
	}
	if body := response.Body.String(); !containsAll(body, `"status":"waiting_identifier"`, `"platform":"hh"`) ||
		containsAll(body, "access_token", "answer") {
		t.Fatalf("body = %s", body)
	}
}

func TestAuthAPIRejectsInvalidRequests(t *testing.T) {
	_, handler := authAPIFixture(t)
	response := performRequest(t, handler, http.MethodPost, "/api/v1/auth/sessions", "", "", map[string]string{"platform": "hh", "unknown": "field"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", response.Code)
	}
	response = performRequest(t, handler, http.MethodPost, "/api/v1/auth/sessions", "", "", authSessionRequest{
		Platform: "hh", ProfileID: "primary", CredentialReference: "file:/x", TTL: "soon",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid ttl status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthAPISubmitsInputAndMapsErrors(t *testing.T) {
	controller, handler := authAPIFixture(t)
	response := performRequest(t, handler, http.MethodPost, "/api/v1/auth/sessions/auth-1/inputs", "", "", authInputRequest{
		Kind: auth.InputIdentifier, Value: "user@example.com",
	})
	if response.Code != http.StatusOK || controller.lastID != "auth-1" || controller.lastInput.Kind != auth.InputIdentifier {
		t.Fatalf("status=%d input=%#v", response.Code, controller.lastInput)
	}
	controller.submitErr = auth.ErrAuthInputMismatch
	response = performRequest(t, handler, http.MethodPost, "/api/v1/auth/sessions/auth-1/inputs", "", "", authInputRequest{
		Kind: auth.InputOTP, Value: "123456",
	})
	if response.Code != http.StatusConflict {
		t.Fatalf("mismatch status = %d", response.Code)
	}
	controller.submitErr = auth.ErrAuthSessionSettled
	response = performRequest(t, handler, http.MethodPost, "/api/v1/auth/sessions/auth-1/inputs", "", "", authInputRequest{
		Kind: auth.InputOTP, Value: "123456",
	})
	if response.Code != http.StatusConflict {
		t.Fatalf("settled status = %d", response.Code)
	}
}

func TestAuthAPIReadsSessionAndChallengePayload(t *testing.T) {
	controller, handler := authAPIFixture(t)
	response := performRequest(t, handler, http.MethodGet, "/api/v1/auth/sessions/auth-1", "", "", nil)
	if response.Code != http.StatusOK || controller.lastID != "auth-1" {
		t.Fatalf("read status=%d body=%s", response.Code, response.Body.String())
	}
	controller.readErr = storage.ErrAuthSessionNotFound
	response = performRequest(t, handler, http.MethodGet, "/api/v1/auth/sessions/missing", "", "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", response.Code)
	}
	controller.payload = auth.ChallengePayload{MediaType: "image/png", Data: []byte("png-bytes")}
	response = performRequest(t, handler, http.MethodGet, "/api/v1/auth/sessions/auth-1/challenge", "", "", nil)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" ||
		response.Body.String() != "png-bytes" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("challenge status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	controller.payloadErr = auth.ErrChallengeNotFound
	response = performRequest(t, handler, http.MethodGet, "/api/v1/auth/sessions/auth-1/challenge", "", "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing payload status = %d", response.Code)
	}
}

func TestAuthAPICancelsSession(t *testing.T) {
	controller, handler := authAPIFixture(t)
	response := performRequest(t, handler, http.MethodPost, "/api/v1/auth/sessions/auth-1/cancel", "", "", nil)
	if response.Code != http.StatusOK || controller.lastID != "auth-1" {
		t.Fatalf("cancel status=%d id=%q", response.Code, controller.lastID)
	}
	controller.cancelErr = auth.ErrAuthSessionSettled
	response = performRequest(t, handler, http.MethodPost, "/api/v1/auth/sessions/auth-1/cancel", "", "", nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("settled cancel status = %d", response.Code)
	}
}

func TestAuthAPIDefaultsBrowserStateFromConfig(t *testing.T) {
	controller, _ := authAPIFixture(t)
	api, err := NewAuthAPI(controller, map[core.ProfileID]string{"primary": "data/profiles/primary.json"})
	if err != nil {
		t.Fatalf("new auth API: %v", err)
	}
	response := performRequest(t, api.Handler(nil), http.MethodPost, "/api/v1/auth/sessions", "", "", authSessionRequest{
		Platform: "hh", ProfileID: "primary",
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if controller.lastStart.BrowserStateReference != "data/profiles/primary.json" {
		t.Fatalf("browser state reference = %q", controller.lastStart.BrowserStateReference)
	}
}
