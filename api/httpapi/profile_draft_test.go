package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

type fakeDraftController struct {
	drafts  map[string]core.ProfileDraft
	applied string
}

type fakeRestarter struct {
	requests int
}

func (restarter *fakeRestarter) RequestRestart() { restarter.requests++ }

func newFakeDraftController() *fakeDraftController {
	return &fakeDraftController{drafts: make(map[string]core.ProfileDraft)}
}

func (controller *fakeDraftController) Create(_ context.Context, tag, adapter string) (core.ProfileDraft, error) {
	if err := core.ValidateProfileDraftTag(tag); err != nil {
		return core.ProfileDraft{}, err
	}
	if _, exists := controller.drafts[tag]; exists {
		return core.ProfileDraft{}, workflow.ErrProfileDraftExists
	}
	if adapter = strings.TrimSpace(adapter); adapter == "" {
		adapter = "hh-main"
	}
	draft, err := core.NewProfileDraft(tag, "hh", adapter, "/store/"+tag+"/state.json", time.Now().UTC())
	if err != nil {
		return core.ProfileDraft{}, err
	}
	controller.drafts[tag] = draft
	return draft, nil
}

func (controller *fakeDraftController) List(context.Context) ([]core.ProfileDraft, error) {
	items := make([]core.ProfileDraft, 0, len(controller.drafts))
	for _, draft := range controller.drafts {
		items = append(items, draft)
	}
	return items, nil
}

func (controller *fakeDraftController) Get(_ context.Context, tag string) (core.ProfileDraft, error) {
	draft, exists := controller.drafts[tag]
	if !exists {
		return core.ProfileDraft{}, storage.ErrProfileDraftNotFound
	}
	return draft, nil
}

func (controller *fakeDraftController) Delete(_ context.Context, tag string) error {
	if _, exists := controller.drafts[tag]; !exists {
		return storage.ErrProfileDraftNotFound
	}
	delete(controller.drafts, tag)
	return nil
}

func (controller *fakeDraftController) CaptureIdentity(_ context.Context, tag string) (core.ProfileDraft, error) {
	draft, exists := controller.drafts[tag]
	if !exists {
		return core.ProfileDraft{}, storage.ErrProfileDraftNotFound
	}
	identity := core.ProfileIdentity{DisplayName: "Антон"}
	if err := draft.ObserveIdentity(identity, []core.ProfileDraftResume{{ID: "resume-9", Title: "Go"}}, time.Now().UTC()); err != nil {
		return core.ProfileDraft{}, err
	}
	controller.drafts[tag] = draft
	return draft, nil
}

func (controller *fakeDraftController) Apply(_ context.Context, tag, primaryResumeID string) (core.ProfileDraft, error) {
	draft, exists := controller.drafts[tag]
	if !exists {
		return core.ProfileDraft{}, storage.ErrProfileDraftNotFound
	}
	controller.applied = primaryResumeID
	if err := draft.MarkApplied(time.Now().UTC()); err != nil {
		return core.ProfileDraft{}, err
	}
	controller.drafts[tag] = draft
	return draft, nil
}

func (controller *fakeDraftController) Directory() string { return "/store" }

func TestProfileDraftAPILifecycle(t *testing.T) {
	controller := newFakeDraftController()
	api, err := NewProfileDraftAPI(controller)
	if err != nil {
		t.Fatalf("new api: %v", err)
	}
	restarter := &fakeRestarter{}
	api.ConfigureRestart(restarter)
	handler := api.Handler(nil)

	create := httptest.NewRequest(http.MethodPost, "/api/v1/profile-drafts", strings.NewReader(`{"tag":"secondary"}`))
	create.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, create)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var draft core.ProfileDraft
	if err := json.NewDecoder(recorder.Body).Decode(&draft); err != nil || draft.Tag != "secondary" {
		t.Fatalf("create draft = %#v err=%v", draft, err)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/profile-drafts", nil))
	var listing struct {
		Items     []core.ProfileDraft `json:"items"`
		Directory string              `json:"directory"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&listing); err != nil || len(listing.Items) != 1 || listing.Directory != "/store" {
		t.Fatalf("list = %#v err=%v", listing, err)
	}

	refresh := httptest.NewRequest(http.MethodPost, "/api/v1/profile-drafts/secondary/refresh", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, refresh)
	if recorder.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	apply := httptest.NewRequest(http.MethodPost, "/api/v1/profile-drafts/secondary/apply", strings.NewReader(`{"primary_resume":"resume-9"}`))
	apply.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, apply)
	if recorder.Code != http.StatusOK || controller.applied != "resume-9" {
		t.Fatalf("apply status = %d applied=%q body=%s", recorder.Code, controller.applied, recorder.Body.String())
	}
	// A plain apply must not restart the runtime.
	if restarter.requests != 0 {
		t.Fatalf("unexpected restart requests = %d", restarter.requests)
	}
	restart := httptest.NewRequest(http.MethodPost, "/api/v1/profile-drafts/secondary/apply", strings.NewReader(`{"primary_resume":"resume-9","restart":true}`))
	restart.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, restart)
	if recorder.Code != http.StatusOK {
		t.Fatalf("restart apply status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var applied profileDraftApplyView
	if err := json.NewDecoder(recorder.Body).Decode(&applied); err != nil || !applied.RestartScheduled {
		t.Fatalf("restart apply view = %#v err=%v", applied, err)
	}
	if restarter.requests != 1 {
		t.Fatalf("restart requests = %d", restarter.requests)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/v1/profile-drafts/secondary", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/v1/profile-drafts/secondary", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d", recorder.Code)
	}
}

func TestProfileDraftAPIErrors(t *testing.T) {
	controller := newFakeDraftController()
	api, err := NewProfileDraftAPI(controller)
	if err != nil {
		t.Fatalf("new api: %v", err)
	}
	handler := api.Handler(nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/profile-drafts/missing", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing draft status = %d", recorder.Code)
	}

	invalid := httptest.NewRequest(http.MethodPost, "/api/v1/profile-drafts", strings.NewReader(`{"tag":"Bad Tag"}`))
	invalid.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, invalid)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid slug status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	first := httptest.NewRequest(http.MethodPost, "/api/v1/profile-drafts", strings.NewReader(`{"tag":"secondary"}`))
	first.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), first)
	duplicate := httptest.NewRequest(http.MethodPost, "/api/v1/profile-drafts", strings.NewReader(`{"tag":"secondary"}`))
	duplicate.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, duplicate)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	if _, err := NewProfileDraftAPI(nil); err == nil {
		t.Fatal("expected a nil controller to fail")
	}
	if !errors.Is(workflow.ErrProfileDraftExists, workflow.ErrProfileDraftExists) {
		t.Fatal("sentinel check")
	}
}
