package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func applicationTailoringFixture(t *testing.T) (ApplicationTailoring, ProfileStateObservation, ProfileStateObservation, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	baseline, err := NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"web":{"keySkills":["Go"]}}}}`), "remote-before", now)
	if err != nil {
		t.Fatalf("new baseline: %v", err)
	}
	tailored, err := NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"web":{"keySkills":["Go","PostgreSQL"]}}}}`), "remote-after", now.Add(time.Second))
	if err != nil {
		t.Fatalf("new tailored observation: %v", err)
	}
	saga, err := NewApplicationTailoring(NewApplicationTailoringParams{
		ID: "tailoring-1", ApplicationID: "application-1", Attempt: 1,
		Key:      ApplicationKey{ProfileID: "primary", Vacancy: VacancyKey{Platform: "hh", ExternalID: "42"}},
		ResumeID: "resume-1", ProcessorTag: "skills-from-vacancy", ProcessorVersion: "v1",
		ProcessorInputDigest: profileStateDigest([]byte("input")),
		AllowedPaths:         []string{"/resumes/resume-1/web/keySkills"},
		Baseline:             baseline, TailoredState: tailored.State,
	}, now)
	if err != nil {
		t.Fatalf("new tailoring: %v", err)
	}
	return saga, baseline, tailored, now
}

func TestApplicationTailoringLifecycleAndPrivateSnapshots(t *testing.T) {
	saga, baseline, tailored, now := applicationTailoringFixture(t)
	if err := saga.BeginApply("apply-proposal", now.Add(time.Second)); err != nil {
		t.Fatalf("begin apply: %v", err)
	}
	if err := saga.RecordApplied(tailored, now.Add(2*time.Second)); err != nil {
		t.Fatalf("record apply: %v", err)
	}
	if err := saga.BeginSubmit(now.Add(3 * time.Second)); err != nil {
		t.Fatalf("begin submit: %v", err)
	}
	if err := saga.BeginRestore("restore-proposal", now.Add(4*time.Second)); err != nil {
		t.Fatalf("begin restore: %v", err)
	}
	if err := saga.RecordRestored(baseline, now.Add(5*time.Second)); err != nil {
		t.Fatalf("record restore: %v", err)
	}
	if saga.Status != ApplicationTailoringRestored || saga.Revision != 6 {
		t.Fatalf("saga=%#v", saga)
	}
	encoded, err := json.Marshal(saga)
	if err != nil {
		t.Fatalf("marshal saga: %v", err)
	}
	if strings.Contains(string(encoded), "PostgreSQL") || strings.Contains(string(encoded), `\"Go\"`) {
		t.Fatalf("public saga leaked snapshot: %s", encoded)
	}
}

func TestApplicationTailoringRejectsReadBackDriftAndRequiresRecovery(t *testing.T) {
	saga, _, _, now := applicationTailoringFixture(t)
	if err := saga.BeginApply("apply-proposal", now.Add(time.Second)); err != nil {
		t.Fatalf("begin apply: %v", err)
	}
	drift, err := NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"web":{"keySkills":["Rust"]}}}}`), "external", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("new drift: %v", err)
	}
	if err := saga.RecordApplied(drift, now.Add(2*time.Second)); err == nil {
		t.Fatal("expected drifted apply read-back to fail")
	}
	if err := saga.RequireRecovery("external profile drift", now.Add(3*time.Second)); err != nil {
		t.Fatalf("require recovery: %v", err)
	}
	if saga.Status != ApplicationTailoringRecoveryRequired {
		t.Fatalf("status=%s", saga.Status)
	}
}

func TestApplicationTailoringRequiresExactAllowedSnapshot(t *testing.T) {
	_, baseline, tailored, now := applicationTailoringFixture(t)
	_, err := NewApplicationTailoring(NewApplicationTailoringParams{
		ID: "tailoring-2", ApplicationID: "application-2", Attempt: 1,
		Key:      ApplicationKey{ProfileID: "primary", Vacancy: VacancyKey{Platform: "hh", ExternalID: "43"}},
		ResumeID: "resume-1", ProcessorTag: "processor", ProcessorVersion: "v1",
		ProcessorInputDigest: profileStateDigest([]byte("input")),
		AllowedPaths:         []string{"/resumes/resume-1/about"}, Baseline: baseline, TailoredState: tailored.State,
	}, now)
	if err == nil {
		t.Fatal("expected mismatched allowed paths to fail")
	}
}

func TestApplicationTailoringRedactedChangesExposeSkillsOnly(t *testing.T) {
	saga, _, _, _ := applicationTailoringFixture(t)
	changes, err := saga.RedactedChanges()
	if err != nil {
		t.Fatalf("redacted changes: %v", err)
	}
	if len(changes) != 1 || changes[0].Path != "/resumes/resume-1/web/keySkills" {
		t.Fatalf("changes=%#v", changes)
	}
	if len(changes[0].Added) != 1 || changes[0].Added[0] != "PostgreSQL" || len(changes[0].Removed) != 0 {
		t.Fatalf("change=%#v", changes[0])
	}
}

func TestApplicationTailoringRedactedChangesHideTextValues(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	baseline, err := NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"about":"before"}}}`), "", now)
	if err != nil {
		t.Fatalf("new baseline: %v", err)
	}
	tailored, err := NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"about":"after"}}}`), "", now)
	if err != nil {
		t.Fatalf("new tailored: %v", err)
	}
	saga, err := NewApplicationTailoring(NewApplicationTailoringParams{
		ID: "tailoring-3", ApplicationID: "application-3", Attempt: 1,
		Key:      ApplicationKey{ProfileID: "primary", Vacancy: VacancyKey{Platform: "hh", ExternalID: "44"}},
		ResumeID: "resume-1", ProcessorTag: "processor", ProcessorVersion: "v1",
		ProcessorInputDigest: profileStateDigest([]byte("input")),
		AllowedPaths:         []string{"/resumes/resume-1/about"}, Baseline: baseline, TailoredState: tailored.State,
	}, now)
	if err != nil {
		t.Fatalf("new tailoring: %v", err)
	}
	changes, err := saga.RedactedChanges()
	if err != nil {
		t.Fatalf("redacted changes: %v", err)
	}
	if len(changes) != 1 || len(changes[0].Added) != 0 || len(changes[0].Removed) != 0 {
		t.Fatalf("changes=%#v", changes)
	}
}
