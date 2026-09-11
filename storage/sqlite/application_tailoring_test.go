package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func sqliteTailoringFixture(t *testing.T, store interface {
	UpsertVacancy(context.Context, core.Vacancy) (bool, error)
	CreateApplication(context.Context, core.Application) (core.Application, bool, error)
}, id core.ApplicationTailoringID, applicationID core.ApplicationID, now time.Time) (core.ApplicationTailoring, core.ProfileStateObservation, core.ProfileStateObservation) {
	t.Helper()
	vacancy := core.Vacancy{
		Platform: "hh", ExternalID: string(applicationID), Title: "Go", State: core.VacancyStateOpen, ObservedAt: now,
	}
	if _, err := store.UpsertVacancy(context.Background(), vacancy); err != nil {
		t.Fatalf("store vacancy: %v", err)
	}
	application, err := core.NewApplication(applicationID, core.ApplicationKey{ProfileID: "primary", Vacancy: vacancy.Key()}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, _, err := store.CreateApplication(context.Background(), application); err != nil {
		t.Fatalf("create application: %v", err)
	}
	baseline, err := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"web":{"keySkills":["Go"]}}}}`), "before", now)
	if err != nil {
		t.Fatalf("new baseline: %v", err)
	}
	tailored, err := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"web":{"keySkills":["Go","SQL"]}}}}`), "after", now.Add(time.Second))
	if err != nil {
		t.Fatalf("new tailored: %v", err)
	}
	saga, err := core.NewApplicationTailoring(core.NewApplicationTailoringParams{
		ID: id, ApplicationID: applicationID, Attempt: 1, Key: application.Key, ResumeID: "resume-1",
		ProcessorTag: "skills", ProcessorVersion: "v1", ProcessorInputDigest: "sha256:" + strings.Repeat("0", 64),
		AllowedPaths: []string{"/resumes/resume-1/web/keySkills"}, Baseline: baseline, TailoredState: tailored.State,
	}, now)
	if err != nil {
		t.Fatalf("new tailoring: %v", err)
	}
	return saga, baseline, tailored
}

func TestStorePersistsTailoringAndEnforcesProfileLease(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	first, baseline, tailored := sqliteTailoringFixture(t, store, "tailoring-1", "application-1", now)
	if _, created, err := store.CreateApplicationTailoring(ctx, first); err != nil || !created {
		t.Fatalf("create first: created=%v err=%v", created, err)
	}
	second, _, _ := sqliteTailoringFixture(t, store, "tailoring-2", "application-2", now)
	if _, _, err := store.CreateApplicationTailoring(ctx, second); !errors.Is(err, storage.ErrProfileMutationLocked) {
		t.Fatalf("second create error=%v, want profile lock", err)
	}

	expected := first.Revision
	if err := first.BeginApply("apply-1", now.Add(time.Second)); err != nil {
		t.Fatalf("begin apply: %v", err)
	}
	if err := store.SaveApplicationTailoring(ctx, first, expected); err != nil {
		t.Fatalf("save applying: %v", err)
	}
	expected = first.Revision
	if err := first.RecordApplied(tailored, now.Add(2*time.Second)); err != nil {
		t.Fatalf("record applied: %v", err)
	}
	if err := store.SaveApplicationTailoring(ctx, first, expected); err != nil {
		t.Fatalf("save applied: %v", err)
	}
	expected = first.Revision
	if err := first.BeginRestore("restore-1", now.Add(3*time.Second)); err != nil {
		t.Fatalf("begin restore: %v", err)
	}
	if err := store.SaveApplicationTailoring(ctx, first, expected); err != nil {
		t.Fatalf("save restoring: %v", err)
	}
	expected = first.Revision
	if err := first.RecordRestored(baseline, now.Add(4*time.Second)); err != nil {
		t.Fatalf("record restored: %v", err)
	}
	if err := store.SaveApplicationTailoring(ctx, first, expected); err != nil {
		t.Fatalf("save restored: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := openStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.ApplicationTailoring(ctx, first.ID)
	if err != nil || persisted.Status != core.ApplicationTailoringRestored || persisted.BaselineDigest != first.BaselineDigest {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	if _, created, err := reopened.CreateApplicationTailoring(ctx, second); err != nil || !created {
		t.Fatalf("create after restore: created=%v err=%v", created, err)
	}
}
