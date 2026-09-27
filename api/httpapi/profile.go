package httpapi

import (
	"errors"
	"net/http"
	"os"
	"time"
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

// ProfileCatalogAPI exposes the declared profiles with their resume list and
// cached identity. It never reads secrets and never contacts the platform.
type ProfileCatalogAPI struct {
	entries []ProfileCatalogEntry
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
	mux.Handle("/", next)
	return mux
}

func (api *ProfileCatalogAPI) list(response http.ResponseWriter, request *http.Request) {
	items := make([]ProfileCatalogEntry, 0, len(api.entries))
	for _, entry := range api.entries {
		items = append(items, entry.withSessionState())
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, listResponse[ProfileCatalogEntry]{Items: items})
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
