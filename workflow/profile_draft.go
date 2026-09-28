package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ErrProfileDraftExists reports a tag that a draft already uses.
var ErrProfileDraftExists = errors.New("profile draft already exists")

// ProfileIdentitySource builds a reader for one profile and its browser state
// file. The backend provides a platform reader; the workflow stays platform
// agnostic.
type ProfileIdentitySource func(profileID core.ProfileID, stateFile string) (adapter.ProfileIdentityReader, error)

// ProfileDraftWorkflow turns dashboard logins into config fragments: it owns
// drafts, captures the account identity after a successful login and writes the
// fragment into the profile store.
type ProfileDraftWorkflow struct {
	mu        sync.RWMutex
	drafts    storage.ProfileDraftRepository
	declared  map[core.ProfileID]struct{}
	adapters  map[string]string
	fallback  string
	directory string
	reader    ProfileIdentitySource
	clock     Clock
}

// SetDeclared replaces the declared profile tags, for example when a config
// reload adds a profile: its tag can no longer be used by a new draft.
func (workflow *ProfileDraftWorkflow) SetDeclared(declared []core.ProfileID) {
	if workflow == nil {
		return
	}
	known := make(map[core.ProfileID]struct{}, len(declared))
	for _, profileID := range declared {
		known[profileID] = struct{}{}
	}
	workflow.mu.Lock()
	workflow.declared = known
	workflow.mu.Unlock()
}

func NewProfileDraftWorkflow(
	drafts storage.ProfileDraftRepository,
	declared []core.ProfileID,
	adapters map[string]string,
	fallbackAdapter string,
	directory string,
	reader ProfileIdentitySource,
	clock Clock,
) (*ProfileDraftWorkflow, error) {
	if drafts == nil || clock == nil {
		return nil, errors.New("profile draft workflow requires drafts and clock")
	}
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("profile draft workflow requires the profile store directory")
	}
	if reader == nil {
		return nil, errors.New("profile draft workflow requires an identity reader source")
	}
	known := make(map[core.ProfileID]struct{}, len(declared))
	for _, profileID := range declared {
		known[profileID] = struct{}{}
	}
	copied := make(map[string]string, len(adapters))
	for tag, platform := range adapters {
		copied[tag] = platform
	}
	return &ProfileDraftWorkflow{
		drafts: drafts, declared: known, adapters: copied,
		fallback: strings.TrimSpace(fallbackAdapter), directory: filepath.Clean(directory),
		reader: reader, clock: clock,
	}, nil
}

// Directory reports the effective profile store directory.
func (workflow *ProfileDraftWorkflow) Directory() string {
	return workflow.directory
}

// ProfileStateFile resolves the browser state target of a draft, so the auth
// API can log in before the profile is declared in the config.
func (workflow *ProfileDraftWorkflow) ProfileStateFile(ctx context.Context, profileID core.ProfileID) (string, bool) {
	draft, err := workflow.drafts.ProfileDraft(ctx, string(profileID))
	if err != nil {
		return "", false
	}
	return draft.StateFile, true
}

// Create registers a pending draft. The state file path derives from the store
// directory, never from a client.
func (workflow *ProfileDraftWorkflow) Create(ctx context.Context, tag, adapterTag string) (core.ProfileDraft, error) {
	tag = strings.TrimSpace(tag)
	if err := core.ValidateProfileDraftTag(tag); err != nil {
		return core.ProfileDraft{}, err
	}
	workflow.mu.RLock()
	_, declared := workflow.declared[core.ProfileID(tag)]
	workflow.mu.RUnlock()
	if declared {
		return core.ProfileDraft{}, fmt.Errorf("profile %q is already declared in the config", tag)
	}
	adapterTag = strings.TrimSpace(adapterTag)
	if adapterTag == "" {
		adapterTag = workflow.fallback
	}
	platform, exists := workflow.adapters[adapterTag]
	if !exists {
		return core.ProfileDraft{}, fmt.Errorf("profile draft references unknown adapter %q", adapterTag)
	}
	now := workflow.clock.Now().UTC()
	sessionDirectory := filepath.Join(workflow.directory, tag)
	if err := os.MkdirAll(sessionDirectory, 0o700); err != nil {
		return core.ProfileDraft{}, fmt.Errorf("create profile session directory %s: %w", sessionDirectory, err)
	}
	stateFile := filepath.Join(sessionDirectory, "state.json")
	draft, err := core.NewProfileDraft(tag, core.Platform(platform), adapterTag, stateFile, now)
	if err != nil {
		return core.ProfileDraft{}, err
	}
	stored, created, err := workflow.drafts.CreateProfileDraft(ctx, draft)
	if err != nil {
		return core.ProfileDraft{}, err
	}
	if !created {
		return stored, fmt.Errorf("%w: %q", ErrProfileDraftExists, tag)
	}
	return stored, nil
}

func (workflow *ProfileDraftWorkflow) List(ctx context.Context) ([]core.ProfileDraft, error) {
	return workflow.drafts.ProfileDrafts(ctx)
}

func (workflow *ProfileDraftWorkflow) Get(ctx context.Context, tag string) (core.ProfileDraft, error) {
	return workflow.drafts.ProfileDraft(ctx, strings.TrimSpace(tag))
}

func (workflow *ProfileDraftWorkflow) Delete(ctx context.Context, tag string) error {
	return workflow.drafts.DeleteProfileDraft(ctx, strings.TrimSpace(tag))
}

// CaptureIdentity refreshes the account summary of a draft through its saved
// session. It runs after a successful login and from the explicit refresh
// endpoint.
func (workflow *ProfileDraftWorkflow) CaptureIdentity(ctx context.Context, tag string) (core.ProfileDraft, error) {
	draft, err := workflow.drafts.ProfileDraft(ctx, strings.TrimSpace(tag))
	if err != nil {
		return core.ProfileDraft{}, err
	}
	if draft.Status == core.ProfileDraftApplied {
		return draft, errors.New("an applied profile draft keeps its identity")
	}
	reader, err := workflow.reader(core.ProfileID(draft.Tag), draft.StateFile)
	if err != nil {
		return core.ProfileDraft{}, err
	}
	snapshot, err := reader.ReadProfileIdentity(ctx, core.ProfileID(draft.Tag))
	if err != nil {
		return core.ProfileDraft{}, err
	}
	identity := core.ProfileIdentity{
		DisplayName: strings.TrimSpace(snapshot.DisplayName),
		Email:       strings.TrimSpace(snapshot.Email),
		Phone:       strings.TrimSpace(snapshot.Phone),
		AccountHash: strings.TrimSpace(snapshot.AccountHash),
		CapturedAt:  snapshot.CapturedAt,
	}
	resumes := make([]core.ProfileDraftResume, 0, len(snapshot.Resumes))
	for _, resume := range snapshot.Resumes {
		if strings.TrimSpace(resume.ID) == "" {
			continue
		}
		resumes = append(resumes, core.ProfileDraftResume{ID: resume.ID, Title: strings.TrimSpace(resume.Title)})
	}
	previous := draft.Revision
	if err := draft.ObserveIdentity(identity, resumes, workflow.clock.Now()); err != nil {
		return core.ProfileDraft{}, err
	}
	if err := workflow.drafts.SaveProfileDraft(ctx, draft, previous); err != nil {
		return core.ProfileDraft{}, err
	}
	return draft, nil
}

// AuthCompleted implements auth.CompletionHook: after a draft login the account
// summary lands in the draft. Declared profiles are ignored.
func (workflow *ProfileDraftWorkflow) AuthCompleted(ctx context.Context, profileID core.ProfileID, browserStateReference string) error {
	draft, err := workflow.drafts.ProfileDraft(ctx, string(profileID))
	if err != nil {
		if errors.Is(err, storage.ErrProfileDraftNotFound) {
			return nil
		}
		return err
	}
	if reference := strings.TrimSpace(browserStateReference); reference != "" && reference != draft.StateFile {
		return nil
	}
	_, err = workflow.CaptureIdentity(ctx, draft.Tag)
	return err
}

// Apply writes the profile fragment into the store directory and marks the
// draft applied. The primary resume must come from the captured list; the first
// resume becomes primary when none is named. Repeating an unchanged apply is
// idempotent.
func (workflow *ProfileDraftWorkflow) Apply(ctx context.Context, tag, primaryResumeID string) (core.ProfileDraft, error) {
	draft, err := workflow.drafts.ProfileDraft(ctx, strings.TrimSpace(tag))
	if err != nil {
		return core.ProfileDraft{}, err
	}
	if draft.Status == core.ProfileDraftPending {
		return core.ProfileDraft{}, errors.New("the profile draft has no captured identity yet; finish the login first")
	}
	resumes, err := fragmentResumes(draft.Resumes, primaryResumeID)
	if err != nil {
		return core.ProfileDraft{}, err
	}
	fragment := profileFragment{Profiles: []profileFragmentProfile{{
		Tag: draft.Tag, Adapter: draft.Adapter, Enabled: true, StateFile: draft.StateFile,
		Identity: fragmentIdentity(draft.Identity), Resumes: resumes,
	}}}
	data, err := json.MarshalIndent(fragment, "", "  ")
	if err != nil {
		return core.ProfileDraft{}, fmt.Errorf("encode profile fragment for %q: %w", draft.Tag, err)
	}
	path := filepath.Join(workflow.directory, draft.Tag+".json")
	if err := writeProfileFragment(path, append(data, '\n')); err != nil {
		return core.ProfileDraft{}, err
	}
	if draft.Status == core.ProfileDraftApplied {
		return draft, nil
	}
	previous := draft.Revision
	if err := draft.MarkApplied(workflow.clock.Now()); err != nil {
		return core.ProfileDraft{}, err
	}
	if err := workflow.drafts.SaveProfileDraft(ctx, draft, previous); err != nil {
		return core.ProfileDraft{}, err
	}
	return draft, nil
}

type profileFragment struct {
	Profiles []profileFragmentProfile `json:"profiles"`
}

type profileFragmentProfile struct {
	Tag       string                   `json:"tag"`
	Adapter   string                   `json:"adapter"`
	Enabled   bool                     `json:"enabled"`
	StateFile string                   `json:"state_file,omitempty"`
	Identity  *profileFragmentIdentity `json:"identity,omitempty"`
	Resumes   []profileFragmentResume  `json:"resumes,omitempty"`
}

type profileFragmentIdentity struct {
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	Phone       string `json:"phone,omitempty"`
	AccountHash string `json:"account_hash,omitempty"`
	CapturedAt  string `json:"captured_at,omitempty"`
}

type profileFragmentResume struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

func fragmentIdentity(identity *core.ProfileIdentity) *profileFragmentIdentity {
	if identity == nil {
		return nil
	}
	result := &profileFragmentIdentity{
		DisplayName: identity.DisplayName, Email: identity.Email,
		Phone: identity.Phone, AccountHash: identity.AccountHash,
	}
	if !identity.CapturedAt.IsZero() {
		result.CapturedAt = identity.CapturedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if *result == (profileFragmentIdentity{}) {
		return nil
	}
	return result
}

func fragmentResumes(captured []core.ProfileDraftResume, primary string) ([]profileFragmentResume, error) {
	primary = strings.TrimSpace(primary)
	list := make([]profileFragmentResume, 0, len(captured))
	found := primary == ""
	for _, resume := range captured {
		id := strings.TrimSpace(resume.ID)
		if id == "" {
			continue
		}
		item := profileFragmentResume{ID: id, Title: strings.TrimSpace(resume.Title), Primary: id == primary}
		if item.Primary {
			found = true
		}
		list = append(list, item)
	}
	if !found {
		return nil, fmt.Errorf("resume %q is not one of the captured resumes", primary)
	}
	if len(list) != 0 && primary == "" {
		list[0].Primary = true
	}
	return list, nil
}

// writeProfileFragment creates the fragment atomically with owner-only
// permissions. An existing fragment is only accepted when its contents match,
// so applying a draft never overwrites a hand-edited file silently.
func writeProfileFragment(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("profile fragment %s already exists with different contents", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read profile fragment %s: %w", path, err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create profile store directory %s: %w", directory, err)
	}
	temp, err := os.CreateTemp(directory, ".fragment-*.json")
	if err != nil {
		return fmt.Errorf("create profile fragment: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write profile fragment: %w", err)
	}
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("protect profile fragment: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close profile fragment: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("store profile fragment %s: %w", path, err)
	}
	return nil
}
