package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/workflow"
)

// ProfileCatalogResume is one resume of the account as the profile fragment
// declares it.
type ProfileCatalogResume struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

// ProfileCatalogIdentity is the cached, non-secret account summary captured at
// login. It lets the operator recognize a profile before the next sign-in.
type ProfileCatalogIdentity struct {
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	Phone       string `json:"phone,omitempty"`
	AccountHash string `json:"account_hash,omitempty"`
	CapturedAt  string `json:"captured_at,omitempty"`
}

// ProfileIdentitySource reads the account summary captured for a profile after
// its sign-in. The config file is read-only, so config profiles keep their
// identity in the database.
type ProfileIdentitySource interface {
	ProfileIdentity(ctx context.Context, profileID core.ProfileID) (core.ProfileIdentity, bool, error)
}

// ProfileCatalogSession reports the browser session file of the profile.
type ProfileCatalogSession struct {
	StateFile  string     `json:"state_file,omitempty"`
	Present    bool       `json:"present"`
	ModifiedAt *time.Time `json:"modified_at,omitempty"`
}

// ProfileCatalogEntry is a declared profile as the dashboard and CLI show it.
// Identity and resumes come from the config or fragment; the session block is
// refreshed from disk on every request.
type ProfileCatalogEntry struct {
	Tag      string                  `json:"tag"`
	Adapter  string                  `json:"adapter"`
	Platform string                  `json:"platform"`
	Enabled  bool                    `json:"enabled"`
	Source   string                  `json:"source"`
	Identity *ProfileCatalogIdentity `json:"identity,omitempty"`
	Resumes  []ProfileCatalogResume  `json:"resumes"`
	Session  ProfileCatalogSession   `json:"session"`
}

// SetImporter attaches the workflow that loads the platform account state.
func (api *ProfileCatalogAPI) SetImporter(importer *workflow.ProfileImportWorkflow) {
	if api == nil {
		return
	}
	api.mu.Lock()
	api.importer = importer
	api.mu.Unlock()
}

// SetIdentitySource attaches the store of captured account summaries. Without
// it the catalog only shows the identity declared in the config or fragment.
func (api *ProfileCatalogAPI) SetIdentitySource(source ProfileIdentitySource) {
	if api == nil {
		return
	}
	api.mu.Lock()
	api.identity = source
	api.mu.Unlock()
}

// ProfileCatalogAPI exposes the declared profiles with their resume list and
// cached identity. It never reads secrets and never contacts the platform.
type ProfileCatalogAPI struct {
	mu       sync.RWMutex
	identity ProfileIdentitySource
	importer *workflow.ProfileImportWorkflow
	entries  []ProfileCatalogEntry
}

// SetEntries replaces the catalog, for example when a config reload adds a
// profile to the running service.
func (api *ProfileCatalogAPI) SetEntries(entries []ProfileCatalogEntry) {
	if api == nil {
		return
	}
	if entries == nil {
		entries = []ProfileCatalogEntry{}
	}
	api.mu.Lock()
	api.entries = entries
	api.mu.Unlock()
}

func NewProfileCatalogAPI(entries []ProfileCatalogEntry) (*ProfileCatalogAPI, error) {
	if entries == nil {
		return nil, errors.New("profile catalog requires entries")
	}
	return &ProfileCatalogAPI{entries: entries}, nil
}

func (api *ProfileCatalogAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/profiles", api.list)
	mux.HandleFunc("POST /api/v1/profiles/{tag}/import", api.importProfile)
	mux.Handle("/", next)
	return mux
}

func (api *ProfileCatalogAPI) list(response http.ResponseWriter, request *http.Request) {
	api.mu.RLock()
	entries := append([]ProfileCatalogEntry(nil), api.entries...)
	identity := api.identity
	api.mu.RUnlock()
	items := make([]ProfileCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		item := entry.withSessionState()
		// A config profile cannot store its captured identity in the read-only
		// file, so the stored summary fills the gap left by the declaration.
		if item.Identity == nil && identity != nil {
			if captured, found, err := identity.ProfileIdentity(request.Context(), core.ProfileID(entry.Tag)); err == nil && found {
				item.Identity = &ProfileCatalogIdentity{
					DisplayName: captured.DisplayName, Email: captured.Email, Phone: captured.Phone,
					AccountHash: captured.AccountHash, CapturedAt: formatIdentityTime(captured.CapturedAt),
				}
			}
		}
		items = append(items, item)
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, listResponse[ProfileCatalogEntry]{Items: items})
}

// formatIdentityTime renders the capture time the way the config declares it.
func formatIdentityTime(captured time.Time) string {
	if captured.IsZero() {
		return ""
	}
	return captured.UTC().Format(time.RFC3339)
}

// importProfile enqueues the account state import: every negotiation and the
// full conversation catalog are loaded into the local database.
func (api *ProfileCatalogAPI) importProfile(response http.ResponseWriter, request *http.Request) {
	requestKey, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	tag := core.ProfileID(strings.TrimSpace(request.PathValue("tag")))
	api.mu.RLock()
	importer := api.importer
	api.mu.RUnlock()
	if tag == "" || importer == nil || !importer.Available(tag) {
		writeProblem(response, http.StatusNotFound, "profile import is not available")
		return
	}
	task, created, err := importer.Enqueue(request.Context(), tag, "dashboard-import", requestKey)
	if err != nil {
		writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusAccepted, map[string]any{"task_id": task.ID, "created": created})
}

// withSessionState refreshes the session file state so a replaced or expired
// state file shows up in the list without a backend restart.
func (entry ProfileCatalogEntry) withSessionState() ProfileCatalogEntry {
	if entry.Session.StateFile == "" {
		return entry
	}
	info, err := os.Stat(entry.Session.StateFile)
	if err != nil {
		entry.Session.Present = false
		entry.Session.ModifiedAt = nil
		return entry
	}
	modified := info.ModTime().UTC()
	entry.Session.Present = true
	entry.Session.ModifiedAt = &modified
	return entry
}
