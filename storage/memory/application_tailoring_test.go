package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func memoryTailoringFixture(t *testing.T, id core.ApplicationTailoringID, applicationID core.ApplicationID, profileID core.ProfileID, now time.Time) (core.ApplicationTailoring, core.ProfileStateObservation, core.ProfileStateObservation) {
	t.Helper()
	baseline, err := core.NewProfileStateObservation(profileID, json.RawMessage(`{"resumes":{"resume-1":{"web":{"keySkills":["Go"]}}}}`), "before", now)
	if err != nil {
		t.Fatalf("new baseline: %v", err)
	}
	tailored, err := core.NewProfileStateObservation(profileID, json.RawMessage(`{"resumes":{"resume-1":{"web":{"keySkills":["Go","SQL"]}}}}`), "after", now.Add(time.Second))
	if err != nil {
		t.Fatalf("new tailored: %v", err)
	}
	saga, err := core.NewApplicationTailoring(core.NewApplicationTailoringParams{
		ID: id, ApplicationID: applicationID, Attempt: 1,
		Key:      core.ApplicationKey{ProfileID: profileID, Vacancy: core.VacancyKey{Platform: "hh", ExternalID: string(applicationID)}},
		ResumeID: "resume-1", ProcessorTag: "skills", ProcessorVersion: "v1",
		ProcessorInputDigest: "sha256:" + strings.Repeat("0", 64),
		AllowedPaths:         []string{"/resumes/resume-1/web/keySkills"}, Baseline: baseline, TailoredState: tailored.State,
	}, now)
	if err != nil {
		t.Fatalf("new tailoring: %v", err)
	}
	return saga, baseline, tailored
}

func TestApplicationTailoringRepositoryLocksProfileUntilRestore(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := NewRepository()
	first, baseline, tailored := memoryTailoringFixture(t, "tailoring-1", "application-1", "primary", now)
	stored, created, err := repository.CreateApplicationTailoring(ctx, first)
	if err != nil || !created {
		t.Fatalf("create first: stored=%#v created=%v err=%v", stored, created, err)
	}
	if repeated, created, err := repository.CreateApplicationTailoring(ctx, first); err != nil || created || repeated.ID != first.ID {
		t.Fatalf("repeat first: stored=%#v created=%v err=%v", repeated, created, err)
	}
	second, _, _ := memoryTailoringFixture(t, "tailoring-2", "application-2", "primary", now)
	if _, _, err := repository.CreateApplicationTailoring(ctx, second); !errors.Is(err, storage.ErrProfileMutationLocked) {
		t.Fatalf("second create error=%v, want profile lock", err)
	}

	expected := first.Revision
	if err := first.BeginApply("apply-1", now.Add(time.Second)); err != nil {
		t.Fatalf("begin apply: %v", err)
	}
	if err := repository.SaveApplicationTailoring(ctx, first, expected); err != nil {
		t.Fatalf("save applying: %v", err)
	}
	expected = first.Revision
	if err := first.RecordApplied(tailored, now.Add(2*time.Second)); err != nil {
		t.Fatalf("record applied: %v", err)
	}
	if err := repository.SaveApplicationTailoring(ctx, first, expected); err != nil {
		t.Fatalf("save applied: %v", err)
	}
	expected = first.Revision
	if err := first.BeginRestore("restore-1", now.Add(3*time.Second)); err != nil {
		t.Fatalf("begin restore: %v", err)
	}
	if err := repository.SaveApplicationTailoring(ctx, first, expected); err != nil {
		t.Fatalf("save restoring: %v", err)
	}
	expected = first.Revision
	if err := first.RecordRestored(baseline, now.Add(4*time.Second)); err != nil {
		t.Fatalf("record restored: %v", err)
	}
	if err := repository.SaveApplicationTailoring(ctx, first, expected); err != nil {
		t.Fatalf("save restored: %v", err)
	}
	if _, created, err := repository.CreateApplicationTailoring(ctx, second); err != nil || !created {
		t.Fatalf("create after restore: created=%v err=%v", created, err)
	}
}
