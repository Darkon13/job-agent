package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestProfileCatalogRefreshesTheSessionState(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "state.json")
	api, err := NewProfileCatalogAPI([]ProfileCatalogEntry{{
		Tag: "primary", Adapter: "hh-main", Platform: "hh", Enabled: true, Source: "config",
		Identity: &ProfileCatalogIdentity{DisplayName: "Антон", Email: "a***@mail.ru"},
		Resumes:  []ProfileCatalogResume{{ID: "resume-9", Title: "Go developer", Primary: true}},
		Session:  ProfileCatalogSession{StateFile: statePath},
	}})
	if err != nil {
		t.Fatalf("new api: %v", err)
	}
	list := func() ProfileCatalogEntry {
		t.Helper()
		recorder := httptest.NewRecorder()
		api.Handler(nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/profiles", nil))
		if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status = %d headers=%v", recorder.Code, recorder.Header())
		}
		var listing struct {
			Items []ProfileCatalogEntry `json:"items"`
		}
		if err := json.NewDecoder(recorder.Body).Decode(&listing); err != nil || len(listing.Items) != 1 {
			t.Fatalf("decode: items=%#v err=%v", listing.Items, err)
		}
		return listing.Items[0]
	}
	entry := list()
	if entry.Session.Present || entry.Session.ModifiedAt != nil {
		t.Fatalf("missing state file = %#v", entry.Session)
	}
	// A raw cookie jar is a secret; the catalog must report only its presence.
	if err := os.WriteFile(statePath, []byte(`{"cookies":[{"value":"secret-cookie"}]}`), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	entry = list()
	if !entry.Session.Present || entry.Session.ModifiedAt == nil {
		t.Fatalf("present state file = %#v", entry.Session)
	}
	if entry.Identity == nil || entry.Identity.DisplayName != "Антон" || len(entry.Resumes) != 1 {
		t.Fatalf("catalog entry = %#v", entry)
	}
	recorder := httptest.NewRecorder()
	api.Handler(nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/profiles", nil))
	if strings.Contains(recorder.Body.String(), "secret-cookie") {
		t.Fatal("the catalog must not expose the session file content")
	}
}

type catalogIdentitySource struct {
	identity core.ProfileIdentity
	found    bool
}

func (source catalogIdentitySource) ProfileIdentity(context.Context, core.ProfileID) (core.ProfileIdentity, bool, error) {
	return source.identity, source.found, nil
}

func TestProfileCatalogFillsTheIdentityCapturedAtLogin(t *testing.T) {
	api, err := NewProfileCatalogAPI([]ProfileCatalogEntry{
		{Tag: "main", Adapter: "hh-main", Platform: "hh", Enabled: true, Source: "config"},
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	api.SetIdentitySource(catalogIdentitySource{
		identity: core.ProfileIdentity{DisplayName: "Антон", Email: "a***@example.test", CapturedAt: time.Date(2026, 9, 28, 16, 0, 0, 0, time.UTC)},
		found:    true,
	})
	response := httptest.NewRecorder()
	api.Handler(http.NotFoundHandler()).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profiles", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"display_name":"Антон"`) || !strings.Contains(body, `"captured_at":"2026-09-28T16:00:00Z"`) {
		t.Fatalf("catalog identity missing: %d %s", response.Code, body)
	}
}

func TestProfileCatalogRequiresEntries(t *testing.T) {
	if _, err := NewProfileCatalogAPI(nil); err == nil {
		t.Fatal("expected a nil entry list to fail")
	}
}
