package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	applicationoperator "github.com/Darkon13/job-agent/operator"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type countingProfileStateReader struct {
	observation core.ProfileStateObservation
	calls       int
}

func (reader *countingProfileStateReader) ReadProfileState(_ context.Context, _ adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
	reader.calls++
	return reader.observation, nil
}

func TestApplicationTailoringWorkflowPlansAndEnqueuesIdempotently(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := memory.NewQueue()
	ids := &sequentialIDs{}
	statePlanner, err := NewProfileStatePlanner(nil, repository, fixedClock{now}, ids)
	if err != nil {
		t.Fatalf("new state planner: %v", err)
	}
	apply, err := NewProfileStateApplyWorkflow(repository, queue, fixedClock{now}, ids, map[core.ProfileID]core.Platform{"primary": "hh"})
	if err != nil {
		t.Fatalf("new apply workflow: %v", err)
	}
	workflow, err := NewApplicationTailoringWorkflow(repository, statePlanner, apply, fixedClock{now}, ids)
	if err != nil {
		t.Fatalf("new tailoring workflow: %v", err)
	}
	application, vacancy := operatorFixtureForTailoring(t, now, []string{"Go", "PostgreSQL"})
	observation, err := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"web":{"keySkills":["Go"]}}}}`), "remote-1", now)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	reader := &countingProfileStateReader{observation: observation}
	processor, _ := applicationoperator.NewAddVacancySkillsProcessor("skills", "v1", 30)
	request := ApplicationTailoringRequest{
		Application: application, Vacancy: vacancy, ResumeID: "resume-1",
		AllowedPaths: []string{"/resumes/resume-1/web/keySkills"}, Processor: processor, Reader: reader,
	}
	first, err := workflow.PlanAndEnqueue(context.Background(), request)
	if err != nil {
		t.Fatalf("first plan: %v", err)
	}
	if first.Tailoring.Status != core.ApplicationTailoringApplying || first.Proposal.ID == "" ||
		first.ApplyTask.Type != core.TaskProfileStateApply || first.ApplyTask.Priority != core.TaskPriorityProfileStateApply {
		t.Fatalf("first result=%#v", first)
	}
	second, err := workflow.PlanAndEnqueue(context.Background(), request)
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}
	if second.Tailoring.ID != first.Tailoring.ID || second.ApplyTask.ID != first.ApplyTask.ID || reader.calls != 1 || len(queue.Tasks()) != 1 {
		t.Fatalf("second=%#v calls=%d tasks=%d", second, reader.calls, len(queue.Tasks()))
	}
}

func TestApplicationTailoringWorkflowSkipsNoopWithoutCreatingSaga(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := memory.NewQueue()
	ids := &sequentialIDs{}
	statePlanner, _ := NewProfileStatePlanner(nil, repository, fixedClock{now}, ids)
	apply, _ := NewProfileStateApplyWorkflow(repository, queue, fixedClock{now}, ids, map[core.ProfileID]core.Platform{"primary": "hh"})
	workflow, _ := NewApplicationTailoringWorkflow(repository, statePlanner, apply, fixedClock{now}, ids)
	application, vacancy := operatorFixtureForTailoring(t, now, []string{"Go"})
	observation, _ := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"web":{"keySkills":["Go"]}}}}`), "remote-1", now)
	processor, _ := applicationoperator.NewAddVacancySkillsProcessor("skills", "v1", 30)
	_, err := workflow.PlanAndEnqueue(context.Background(), ApplicationTailoringRequest{
		Application: application, Vacancy: vacancy, ResumeID: "resume-1",
		AllowedPaths: []string{"/resumes/resume-1/web/keySkills"}, Processor: processor,
		Reader: &countingProfileStateReader{observation: observation},
	})
	if !errors.Is(err, ErrApplicationTailoringNoChanges) || len(queue.Tasks()) != 0 {
		t.Fatalf("error=%v tasks=%d", err, len(queue.Tasks()))
	}
}

func operatorFixtureForTailoring(t *testing.T, now time.Time, skills []string) (core.Application, core.Vacancy) {
	t.Helper()
	key := core.VacancyKey{Platform: "hh", ExternalID: "42"}
	application, err := core.NewApplication("application-1", core.ApplicationKey{ProfileID: "primary", Vacancy: key}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	vacancy := core.Vacancy{
		Platform: key.Platform, ExternalID: key.ExternalID, Title: "Go developer", Employer: "Example",
		State: core.VacancyStateOpen, ObservedAt: now, Attributes: map[string]any{"key_skills": skills},
	}
	return application, vacancy
}
