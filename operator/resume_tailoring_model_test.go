package operator

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeResumeTailoringModel struct {
	response ResumeTailoringModelResponse
	err      error
	errs     []error
	calls    int
	request  ResumeTailoringModelRequest
}

func (model *fakeResumeTailoringModel) Select(_ context.Context, request ResumeTailoringModelRequest) (ResumeTailoringModelResponse, error) {
	model.calls++
	model.request = request
	if index := model.calls - 1; index < len(model.errs) && model.errs[index] != nil {
		return ResumeTailoringModelResponse{}, model.errs[index]
	}
	if model.err != nil {
		return ResumeTailoringModelResponse{}, model.err
	}
	return model.response, nil
}

func modelTailoringProcessor(t *testing.T, maximum int, model ResumeTailoringModel) *ModelResumeTailoringProcessor {
	t.Helper()
	return modelTailoringProcessorWithRemovals(t, maximum, false, model)
}

func modelTailoringProcessorWithRemovals(t *testing.T, maximum int, allowRemovals bool, model ResumeTailoringModel) *ModelResumeTailoringProcessor {
	t.Helper()
	fallback, err := NewAddVacancySkillsProcessor("skills-from-vacancy", "v1", maximum)
	if err != nil {
		t.Fatalf("new fallback: %v", err)
	}
	processor, err := NewModelResumeTailoringProcessor(ModelResumeTailoringConfig{
		Tag: "openai-test", PromptVersion: "v1", Instruction: "Prefer Go skills",
		MaximumSkills: maximum, AllowRemovals: allowRemovals, Timeout: 5 * time.Second, Model: model, Fallback: fallback,
	})
	if err != nil {
		t.Fatalf("new processor: %v", err)
	}
	return processor
}

func decodeTailoredSkills(t *testing.T, plan ResumeTailoringPlan) []string {
	t.Helper()
	if len(plan.Overrides) != 1 {
		t.Fatalf("overrides=%#v", plan.Overrides)
	}
	var skills []string
	if err := json.Unmarshal(plan.Overrides[0].Value, &skills); err != nil {
		t.Fatalf("decode skills: %v", err)
	}
	return skills
}

func TestModelResumeTailoringSelectsSubsetWithinLimit(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go"}, []string{"Go", "PostgreSQL", "Kafka"})
	model := &fakeResumeTailoringModel{response: ResumeTailoringModelResponse{Skills: []ResumeTailoringModelDecision{
		{Value: "Go", Action: ResumeTailoringSkillKeep, Evidence: "resume.skills"},
		{Value: "PostgreSQL", Action: ResumeTailoringSkillAdd, Evidence: "vacancy.key_skills"},
	}}}
	plan, err := modelTailoringProcessor(t, 2, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := decodeTailoredSkills(t, plan); !reflect.DeepEqual(got, []string{"Go", "PostgreSQL"}) {
		t.Fatalf("skills=%#v", got)
	}
	if plan.ProcessorTag != "openai-test" || plan.ProcessorVersion != "v1" {
		t.Fatalf("provenance tag=%q version=%q", plan.ProcessorTag, plan.ProcessorVersion)
	}
	if model.request.MaximumSkills != 2 || !reflect.DeepEqual(model.request.VacancySkills, []string{"Go", "PostgreSQL", "Kafka"}) {
		t.Fatalf("model request=%#v", model.request)
	}
}

func TestModelResumeTailoringFallsBackOnModelError(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go"}, []string{"PostgreSQL", "Kafka"})
	model := &fakeResumeTailoringModel{err: errors.New("provider unavailable")}
	plan, err := modelTailoringProcessor(t, 10, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.ProcessorTag != "skills-from-vacancy" {
		t.Fatalf("expected deterministic fallback, tag=%q", plan.ProcessorTag)
	}
	if got := decodeTailoredSkills(t, plan); !reflect.DeepEqual(got, []string{"Go", "PostgreSQL", "Kafka"}) {
		t.Fatalf("skills=%#v", got)
	}
}

func TestModelResumeTailoringFallsBackOnUnknownSkill(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go"}, []string{"PostgreSQL"})
	model := &fakeResumeTailoringModel{response: ResumeTailoringModelResponse{Skills: []ResumeTailoringModelDecision{
		{Value: "Rust", Action: ResumeTailoringSkillAdd, Evidence: "vacancy.key_skills"},
	}}}
	plan, err := modelTailoringProcessor(t, 10, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.ProcessorTag != "skills-from-vacancy" {
		t.Fatalf("expected deterministic fallback, tag=%q", plan.ProcessorTag)
	}
}

func TestModelResumeTailoringDoesNotDuplicateExistingSkill(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go"}, []string{"Go", "PostgreSQL"})
	model := &fakeResumeTailoringModel{response: ResumeTailoringModelResponse{Skills: []ResumeTailoringModelDecision{
		{Value: "go", Action: ResumeTailoringSkillAdd, Evidence: "vacancy.key_skills"},
		{Value: "PostgreSQL", Action: ResumeTailoringSkillAdd, Evidence: "vacancy.key_skills"},
	}}}
	plan, err := modelTailoringProcessor(t, 10, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := decodeTailoredSkills(t, plan); !reflect.DeepEqual(got, []string{"Go", "PostgreSQL"}) {
		t.Fatalf("skills=%#v", got)
	}
	if plan.Skills[0].Action != ResumeTailoringSkillKeep {
		t.Fatalf("existing skill action=%q", plan.Skills[0].Action)
	}
}

func TestModelResumeTailoringRemovesLeastRelevantSkillWhenAllowed(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go", "Linux", "C#"}, []string{"Go", "PostgreSQL"})
	model := &fakeResumeTailoringModel{response: ResumeTailoringModelResponse{Skills: []ResumeTailoringModelDecision{
		{Value: "Go", Action: ResumeTailoringSkillKeep, Evidence: "resume.skills"},
		{Value: "C#", Action: ResumeTailoringSkillRemove, Evidence: "vacancy.key_skills"},
		{Value: "PostgreSQL", Action: ResumeTailoringSkillAdd, Evidence: "vacancy.key_skills"},
	}}}
	plan, err := modelTailoringProcessorWithRemovals(t, 10, true, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := decodeTailoredSkills(t, plan); !reflect.DeepEqual(got, []string{"Go", "Linux", "PostgreSQL"}) {
		t.Fatalf("skills=%#v", got)
	}
	if len(plan.Skills) != 3 || plan.Skills[1].Action != ResumeTailoringSkillRemove {
		t.Fatalf("decisions=%#v", plan.Skills)
	}
	if !strings.Contains(model.request.Instruction, "remove") {
		t.Fatalf("instruction=%q", model.request.Instruction)
	}
}

func TestModelResumeTailoringRemovalRequiresPolicy(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go", "Linux"}, []string{"PostgreSQL"})
	model := &fakeResumeTailoringModel{response: ResumeTailoringModelResponse{Skills: []ResumeTailoringModelDecision{
		{Value: "Go", Action: ResumeTailoringSkillKeep, Evidence: "resume.skills"},
		{Value: "Linux", Action: ResumeTailoringSkillRemove, Evidence: "vacancy.key_skills"},
		{Value: "PostgreSQL", Action: ResumeTailoringSkillAdd, Evidence: "vacancy.key_skills"},
	}}}
	plan, err := modelTailoringProcessor(t, 10, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.ProcessorTag != "skills-from-vacancy" {
		t.Fatalf("expected deterministic fallback, tag=%q", plan.ProcessorTag)
	}
	if got := decodeTailoredSkills(t, plan); !reflect.DeepEqual(got, []string{"Go", "Linux", "PostgreSQL"}) {
		t.Fatalf("skills=%#v", got)
	}
	if !strings.Contains(model.request.Instruction, "Never remove") {
		t.Fatalf("instruction=%q", model.request.Instruction)
	}
}

func TestModelResumeTailoringRejectsRemovingEverySkill(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go", "Linux"}, []string{"PostgreSQL"})
	model := &fakeResumeTailoringModel{response: ResumeTailoringModelResponse{Skills: []ResumeTailoringModelDecision{
		{Value: "Go", Action: ResumeTailoringSkillRemove, Evidence: "vacancy.key_skills"},
		{Value: "Linux", Action: ResumeTailoringSkillRemove, Evidence: "vacancy.key_skills"},
	}}}
	plan, err := modelTailoringProcessorWithRemovals(t, 10, true, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.ProcessorTag != "skills-from-vacancy" {
		t.Fatalf("expected deterministic fallback, tag=%q", plan.ProcessorTag)
	}
}

func TestModelResumeTailoringRejectsRemovingVacancyOnlySkill(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go"}, []string{"PostgreSQL"})
	model := &fakeResumeTailoringModel{response: ResumeTailoringModelResponse{Skills: []ResumeTailoringModelDecision{
		{Value: "PostgreSQL", Action: ResumeTailoringSkillRemove, Evidence: "vacancy.key_skills"},
	}}}
	plan, err := modelTailoringProcessorWithRemovals(t, 10, true, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.ProcessorTag != "skills-from-vacancy" {
		t.Fatalf("expected deterministic fallback, tag=%q", plan.ProcessorTag)
	}
}

func TestModelResumeTailoringTemporaryFailureKeepsCurrentSkills(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go", "Linux"}, []string{"PostgreSQL"})
	model := &fakeResumeTailoringModel{errs: []error{
		&ModelError{Kind: ModelFailureRateLimited, Operation: "test", Message: "rate limited"},
		&ModelError{Kind: ModelFailureTemporary, Operation: "test", Message: "provider unavailable"},
	}}
	plan, err := modelTailoringProcessor(t, 10, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.ProcessorTag != "openai-test" || len(plan.Overrides) != 0 {
		t.Fatalf("expected empty model plan, tag=%q overrides=%#v", plan.ProcessorTag, plan.Overrides)
	}
	if model.calls != 2 {
		t.Fatalf("calls=%d, want retry", model.calls)
	}
}

func TestModelResumeTailoringRetriesAfterRateLimit(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go"}, []string{"PostgreSQL"})
	model := &fakeResumeTailoringModel{
		errs: []error{&ModelError{Kind: ModelFailureRateLimited, Operation: "test", Message: "rate limited"}},
		response: ResumeTailoringModelResponse{Skills: []ResumeTailoringModelDecision{
			{Value: "PostgreSQL", Action: ResumeTailoringSkillAdd, Evidence: "vacancy.key_skills"},
		}},
	}
	plan, err := modelTailoringProcessor(t, 10, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := decodeTailoredSkills(t, plan); !reflect.DeepEqual(got, []string{"Go", "PostgreSQL"}) {
		t.Fatalf("skills=%#v", got)
	}
	if model.calls != 2 {
		t.Fatalf("calls=%d, want retry", model.calls)
	}
}

func TestModelResumeTailoringReturnsFallbackLimitError(t *testing.T) {
	input := resumeTailoringFixture(t, []string{"Go"}, []string{"PostgreSQL", "Kafka"})
	model := &fakeResumeTailoringModel{response: ResumeTailoringModelResponse{Skills: []ResumeTailoringModelDecision{
		{Value: "PostgreSQL", Action: ResumeTailoringSkillAdd, Evidence: "vacancy.key_skills"},
		{Value: "Kafka", Action: ResumeTailoringSkillAdd, Evidence: "vacancy.key_skills"},
	}}}
	_, err := modelTailoringProcessor(t, 2, model).Plan(context.Background(), input)
	if !errors.Is(err, ErrResumeTailoringSkillLimit) {
		t.Fatalf("error=%v, want skill limit", err)
	}
}
