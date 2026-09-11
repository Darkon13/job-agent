package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/auth"
	"github.com/Darkon13/job-agent/core"
)

type fakeAuthEventsController struct {
	mu       sync.Mutex
	sessions []core.AuthSession
	index    int
}

func (controller *fakeAuthEventsController) next() core.AuthSession {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.index >= len(controller.sessions) {
		return controller.sessions[len(controller.sessions)-1]
	}
	session := controller.sessions[controller.index]
	if controller.index < len(controller.sessions)-1 {
		controller.index++
	}
	return session
}

func (controller *fakeAuthEventsController) Start(context.Context, auth.StartRequest) (core.AuthSession, error) {
	panic("not used")
}

func (controller *fakeAuthEventsController) Submit(context.Context, core.AuthSessionID, auth.Input) (core.AuthSession, error) {
	panic("not used")
}

func (controller *fakeAuthEventsController) Cancel(context.Context, core.AuthSessionID) (core.AuthSession, error) {
	panic("not used")
}

func (controller *fakeAuthEventsController) Session(context.Context, core.AuthSessionID) (core.AuthSession, error) {
	return controller.next(), nil
}

func (controller *fakeAuthEventsController) ChallengePayload(context.Context, core.AuthSessionID) (auth.ChallengePayload, error) {
	panic("not used")
}

func TestAuthEventsStreamsUntilTerminal(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	controller := &fakeAuthEventsController{sessions: []core.AuthSession{
		{ID: "auth-1", Platform: "hh", ProfileID: "primary", Status: core.AuthSessionWaitingOTP, Revision: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "auth-1", Platform: "hh", ProfileID: "primary", Status: core.AuthSessionCompleted, Revision: 2, CreatedAt: now, UpdatedAt: now},
	}}
	api, err := NewAuthAPI(controller)
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	api.eventsInterval = 5 * time.Millisecond
	response := httptest.NewRecorder()
	api.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/sessions/auth-1/events", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("content type = %q", contentType)
	}
	rendered := response.Body.String()
	if strings.Count(rendered, "event: session") != 2 {
		t.Fatalf("events = %q", rendered)
	}
	if !strings.Contains(rendered, `"status":"completed"`) || !strings.Contains(rendered, `"revision":2`) {
		t.Fatalf("terminal event missing: %q", rendered)
	}
}
