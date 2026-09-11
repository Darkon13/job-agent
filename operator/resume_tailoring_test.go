package operator

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func resumeTailoringFixture(t *testing.T, currentSkills, vacancySkills []string) ResumeTailoringInput {
	t.Helper()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	application, err := core.NewApplication("application-1", core.ApplicationKey{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	vacancy := core.Vacancy{
		Platform: "hh", ExternalID: "42", Title: "Go developer", Employer: "Example",
		State: core.VacancyStateOpen, ObservedAt: now,
		Attributes: map[string]any{"key_skills": vacancySkills},
	}
	state, err := json.Marshal(map[string]any{
		"resumes": map[string]any{"resume-1": map[string]any{
			"web": map[string]any{"keySkills": currentSkills},
		}},
	})
	if err != nil {
		t.Fatalf("marshal current state: %v", err)
	}
	observation, err := core.NewProfileStateObservation("primary", state, "revision-1", now)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	return ResumeTailoringInput{
		Application: application, Vacancy: vacancy, ResumeID: "resume-1",
		EmployerGroups: []string{"marketplaces"}, CurrentState: observation,
		AllowedPaths: []string{"/resumes/resume-1/web/keySkills"},
	}
}

func TestAddVacancySkillsProcessorPreservesCurrentAndAddsAllVacancySkills(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go", "Linux"}, []string{"go", "PostgreSQL", " Redis ", "PostgreSQL"})
	processor, err := NewAddVacancySkillsProcessor("skills-from-vacancy", "v1", 10)
	if err != nil {
		t.Fatalf("new processor: %v", err)
	}
	plan, err := processor.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Overrides) != 1 || plan.Overrides[0].Path != "/resumes/resume-1/web/keySkills" {
		t.Fatalf("overrides=%#v", plan.Overrides)
	}
	var skills []string
	if err := json.Unmarshal(plan.Overrides[0].Value, &skills); err != nil {
		t.Fatalf("decode skills: %v", err)
	}
	want := []string{"Go", "Linux", "PostgreSQL", "Redis"}
	if !reflect.DeepEqual(skills, want) {
		t.Fatalf("skills=%#v, want %#v", skills, want)
	}
	wantActions := []string{ResumeTailoringSkillKeep, ResumeTailoringSkillAdd, ResumeTailoringSkillAdd}
	if len(plan.Skills) != len(wantActions) {
		t.Fatalf("decisions=%#v", plan.Skills)
	}
	for index, action := range wantActions {
		if plan.Skills[index].Action != action || plan.Skills[index].Evidence != "vacancy.key_skills" {
			t.Fatalf("decision[%d]=%#v", index, plan.Skills[index])
		}
	}
	if !validResumeTailoringDigest(plan.InputDigest) {
		t.Fatalf("input digest=%q", plan.InputDigest)
	}
}

func TestAddVacancySkillsProcessorDoesNotRewriteAnEquivalentSet(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go", "PostgreSQL"}, []string{"go", "postgresql"})
	processor, _ := NewAddVacancySkillsProcessor("skills-from-vacancy", "v1", 10)
	plan, err := processor.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Overrides) != 0 {
		t.Fatalf("equivalent skills produced overrides: %#v", plan.Overrides)
	}
}

func TestAddVacancySkillsProcessorRefusesSilentTruncation(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go", "Linux"}, []string{"PostgreSQL", "Redis"})
	processor, _ := NewAddVacancySkillsProcessor("skills-from-vacancy", "v1", 3)
	_, err := processor.Plan(context.Background(), input)
	if !errors.Is(err, ErrResumeTailoringSkillLimit) {
		t.Fatalf("error=%v, want skill limit", err)
	}
}

func TestAddVacancySkillsProcessorRejectsDisallowedPathAndInvalidCurrentState(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go"}, []string{"PostgreSQL"})
	processor, _ := NewAddVacancySkillsProcessor("skills-from-vacancy", "v1", 10)
	input.AllowedPaths = []string{"/resumes/resume-1/about"}
	if _, err := processor.Plan(context.Background(), input); err == nil {
		t.Fatal("expected disallowed skill path to fail")
	}

	input = resumeTailoringFixture(t, []string{"Go"}, []string{"PostgreSQL"})
	bad, err := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"resume-1":{"web":{"keySkills":"Go"}}}}`), "revision-2", input.CurrentState.ObservedAt)
	if err != nil {
		t.Fatalf("new bad observation: %v", err)
	}
	input.CurrentState = bad
	if _, err := processor.Plan(context.Background(), input); err == nil {
		t.Fatal("expected non-array current skills to fail")
	}
}
