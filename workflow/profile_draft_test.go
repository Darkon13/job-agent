package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type fakeIdentityReader struct {
	snapshot adapter.ProfileIdentitySnapshot
	err      error
}

func (reader fakeIdentityReader) ReadProfileIdentity(context.Context, core.ProfileID) (adapter.ProfileIdentitySnapshot, error) {
	return reader.snapshot, reader.err
}

func newProfileDraftFixture(t *testing.T, reader adapter.ProfileIdentityReader) (*ProfileDraftWorkflow, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "profile-store")
	workflow, err := NewProfileDraftWorkflow(
		storagememory.NewRepository(),
		[]core.ProfileID{"primary"},
		map[string]string{"hh-main": "hh"},
		"hh-main",
		directory,
		func(core.ProfileID, string) (adapter.ProfileIdentityReader, error) { return reader, nil },
		SystemClock{},
	)
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	return workflow, directory
}

func TestProfileDraftCreateDerivesTheSessionPath(t *testing.T) {
	workflow, directory := newProfileDraftFixture(t, fakeIdentityReader{})
	ctx := context.Background()
	draft, err := workflow.Create(ctx, "secondary", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	wantState := filepath.Join(directory, "secondary", "state.json")
	if draft.StateFile != wantState || draft.Status != core.ProfileDraftPending || draft.Platform != "hh" {
		t.Fatalf("draft = %#v", draft)
	}
	if info, err := os.Stat(filepath.Dir(wantState)); err != nil || !info.IsDir() {
		t.Fatalf("session directory: %v", err)
	}
	if _, err := workflow.Create(ctx, "secondary", ""); !errors.Is(err, ErrProfileDraftExists) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := workflow.Create(ctx, "primary", ""); err == nil || !strings.Contains(err.Error(), "already declared") {
		t.Fatalf("declared tag error = %v", err)
	}
	if _, err := workflow.Create(ctx, "Bad Tag", ""); err == nil {
		t.Fatal("expected an invalid slug to fail")
	}
}

func TestProfileDraftCaptureAndApplyWriteTheFragment(t *testing.T) {
	reader := fakeIdentityReader{snapshot: adapter.ProfileIdentitySnapshot{
		DisplayName: "Антон Шумаков", Email: "u***@example.test", Phone: "79*******67",
		AccountHash: "sha256:abcdef123456",
		Resumes: []adapter.ProfileIdentityResume{
			{ID: "resume-9", Title: "Go developer"},
			{ID: "resume-8"},
		},
		CapturedAt: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
	}}
	workflow, directory := newProfileDraftFixture(t, reader)
	ctx := context.Background()
	if _, err := workflow.Create(ctx, "secondary", "hh-main"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := workflow.Apply(ctx, "secondary", ""); err == nil {
		t.Fatal("a pending draft must not apply")
	}
	draft, err := workflow.CaptureIdentity(ctx, "secondary")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if draft.Status != core.ProfileDraftReady || draft.Identity == nil || draft.Identity.DisplayName != "Антон Шумаков" {
		t.Fatalf("draft = %#v", draft)
	}
	if len(draft.Resumes) != 2 || draft.Resumes[0].ID != "resume-9" {
		t.Fatalf("resumes = %#v", draft.Resumes)
	}
	if _, err := workflow.Apply(ctx, "secondary", "resume-missing"); err == nil {
		t.Fatal("an unknown primary resume must not apply")
	}
	applied, err := workflow.Apply(ctx, "secondary", "resume-8")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Status != core.ProfileDraftApplied {
		t.Fatalf("applied draft = %#v", applied)
	}
	path := filepath.Join(directory, "secondary.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("fragment file: %v mode=%v", err, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fragment: %v", err)
	}
	var fragment struct {
		Profiles []struct {
			Tag       string `json:"tag"`
			Adapter   string `json:"adapter"`
			Enabled   bool   `json:"enabled"`
			StateFile string `json:"state_file"`
			Identity  *struct {
				DisplayName string `json:"display_name"`
				CapturedAt  string `json:"captured_at"`
			} `json:"identity"`
			Resumes []struct {
				ID      string `json:"id"`
				Primary bool   `json:"primary"`
			} `json:"resumes"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &fragment); err != nil {
		t.Fatalf("decode fragment: %v\n%s", err, data)
	}
	if len(fragment.Profiles) != 1 {
		t.Fatalf("fragment = %s", data)
	}
	profile := fragment.Profiles[0]
	if profile.Tag != "secondary" || profile.Adapter != "hh-main" || !profile.Enabled {
		t.Fatalf("fragment profile = %#v", profile)
	}
	if profile.StateFile != filepath.Join(directory, "secondary", "state.json") {
		t.Fatalf("fragment state file = %q", profile.StateFile)
	}
	if profile.Identity == nil || profile.Identity.DisplayName != "Антон Шумаков" || profile.Identity.CapturedAt == "" {
		t.Fatalf("fragment identity = %#v", profile.Identity)
	}
	if len(profile.Resumes) != 2 || profile.Resumes[0].Primary || !profile.Resumes[1].Primary {
		t.Fatalf("fragment resumes = %#v", profile.Resumes)
	}
	// Repeating an unchanged apply is idempotent and keeps the fragment.
	if _, err := workflow.Apply(ctx, "secondary", "resume-8"); err != nil {
		t.Fatalf("repeat apply: %v", err)
	}
}

func TestProfileDraftAuthCompletedIgnoresUnknownProfiles(t *testing.T) {
	reader := fakeIdentityReader{snapshot: adapter.ProfileIdentitySnapshot{
		Resumes: []adapter.ProfileIdentityResume{{ID: "resume-9", Title: "Go"}},
	}}
	workflow, _ := newProfileDraftFixture(t, reader)
	ctx := context.Background()
	if _, err := workflow.Create(ctx, "secondary", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := workflow.AuthCompleted(ctx, "primary", ""); err != nil {
		t.Fatalf("declared profile hook: %v", err)
	}
	if err := workflow.AuthCompleted(ctx, "secondary", ""); err != nil {
		t.Fatalf("draft hook: %v", err)
	}
	draft, err := workflow.Get(ctx, "secondary")
	if err != nil || draft.Status != core.ProfileDraftReady || len(draft.Resumes) != 1 {
		t.Fatalf("draft after hook = %#v err=%v", draft, err)
	}
	if _, err := workflow.Get(ctx, "secondary"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := workflow.Delete(ctx, "secondary"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := workflow.Get(ctx, "secondary"); err == nil {
		t.Fatal("expected the deleted draft to be gone")
	}
}
