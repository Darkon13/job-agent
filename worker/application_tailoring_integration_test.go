package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	applicationoperator "github.com/Darkon13/job-agent/operator"
)

type countingTailoringIDs struct{ next int }

func (ids *countingTailoringIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}

// fakeProfileStateSession is both the trusted reader and writer for one
// profile. ApplyProfileState mutates the same in-memory state that reads
// observe, so apply and restore are verified against real read-backs.
type fakeProfileStateSession struct {
	profileID      core.ProfileID
	state          json.RawMessage
	remoteRevision string
	observedAt     time.Time
	reads          int
	applies        int
}

func (session *fakeProfileStateSession) ReadProfileState(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
	session.reads++
	if request.ProfileID != session.profileID {
		return core.ProfileStateObservation{}, fmt.Errorf("unexpected profile %q", request.ProfileID)
	}
	return core.NewProfileStateObservation(session.profileID, session.state, session.remoteRevision, session.observedAt)
}

func (session *fakeProfileStateSession) ApplyProfileState(_ context.Context, proposal core.ProfileStateProposal) (adapter.ProfileStateApplyResult, error) {
	session.applies++
	session.state = append(json.RawMessage(nil), proposal.DesiredState...)
	session.remoteRevision = fmt.Sprintf("revision-%d", session.applies)
	observation, err := core.NewProfileStateObservation(session.profileID, session.state, session.remoteRevision, session.observedAt)
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	return adapter.ProfileStateApplyResult{Observation: observation}, nil
}

func tailoringFixture(t *testing.T, transport *fakeApplicationTransport) (*ApplicationHandler, *fakeProfileStateSession, core.Task) {
	t.Helper()
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	processor, err := applicationoperator.NewAddVacancySkillsProcessor("skills-from-vacancy", "v1", 30)
	if err != nil {
		t.Fatalf("build processor: %v", err)
	}
	tailoringPlan := ApplicationTailoringPlan{
		Processor: processor, AllowedPaths: []string{applicationoperator.ResumeSkillsPath("resume-1")},
	}
	plan := liveApplicationPlan("resume-1")
	plan.Tailoring = &tailoringPlan
	plans := StaticApplicationPlans{"profile-1": plan}
	handler, repository, task, clock := applicationFixture(t, plans, transport)
	session := &fakeProfileStateSession{
		profileID: "profile-1", observedAt: now, remoteRevision: "revision-1",
		state: json.RawMessage(`{"resumes":{"resume-1":{"web":{"keySkills":["Go"]}}}}`),
	}
	readers := map[core.ProfileID]adapter.ProfileStateReader{"profile-1": session}
	writers := NewProfileStateWriterRegistry()
	if err := writers.Register("profile-1", session); err != nil {
		t.Fatalf("register writer: %v", err)
	}
	coordinator, err := NewApplicationTailoringCoordinator(repository, repository, readers, writers, clock, &countingTailoringIDs{})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	handler.ConfigureTailoring(coordinator)
	return handler, session, task
}

func TestApplicationHandlerAppliesAndRestoresTailoring(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	transport := &fakeApplicationTransport{vacancy: core.Vacancy{
		Platform: "hh", ExternalID: "42", Title: "Go developer",
		State: core.VacancyStateOpen, ObservedAt: now,
		Attributes: map[string]any{"key_skills": []any{"Go", "PostgreSQL"}},
	}}
	handler, session, task := tailoringFixture(t, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("expected one submit, got %d", transport.calls)
	}
	if session.applies != 2 {
		t.Fatalf("expected apply and restore writes, got %d", session.applies)
	}
	var state struct {
		Resumes map[string]struct {
			Web struct {
				KeySkills []string `json:"keySkills"`
			} `json:"web"`
		} `json:"resumes"`
	}
	if err := json.Unmarshal(session.state, &state); err != nil {
		t.Fatalf("decode restored state: %v", err)
	}
	if got := state.Resumes["resume-1"].Web.KeySkills; !slices.Equal(got, []string{"Go"}) {
		t.Fatalf("expected baseline skills after restore, got %v", got)
	}
}

func TestApplicationHandlerSkipsTailoringWithoutNewSkills(t *testing.T) {
	now := time.Date(2026, 7, 19, 14, 0, 0, 0, time.UTC)
	transport := &fakeApplicationTransport{vacancy: core.Vacancy{
		Platform: "hh", ExternalID: "42", Title: "Go developer",
		State: core.VacancyStateOpen, ObservedAt: now,
		Attributes: map[string]any{"key_skills": []any{"Go"}},
	}}
	handler, session, task := tailoringFixture(t, transport)
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("expected one submit, got %d", transport.calls)
	}
	if session.applies != 0 {
		t.Fatalf("expected no profile writes without new skills, got %d", session.applies)
	}
}
