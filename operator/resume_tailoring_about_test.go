package operator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

type fakeResumeTailoringAboutModel struct {
	response ResumeTailoringAboutResponse
	err      error
	calls    int
	request  ResumeTailoringAboutRequest
}

func (model *fakeResumeTailoringAboutModel) RewriteAbout(_ context.Context, request ResumeTailoringAboutRequest) (ResumeTailoringAboutResponse, error) {
	model.calls++
	model.request = request
	if model.err != nil {
		return ResumeTailoringAboutResponse{}, model.err
	}
	return model.response, nil
}

func resumeTailoringAboutFacts() map[string]any {
	return map[string]any{
		"placeholders": map[string]any{"name": "Иван"},
		"position":     "Go developer",
		"city":         "Москва",
	}
}

func resumeTailoringAboutFixture(t *testing.T) ResumeTailoringInput {
	t.Helper()
	input := resumeTailoringFixture(t, []string{"Go"}, []string{"PostgreSQL"})
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	state, err := json.Marshal(map[string]any{
		"resumes": map[string]any{"resume-1": map[string]any{
			"web": map[string]any{"keySkills": []string{"Go"}, "skills": []string{"Иван пишет на Go"}},
		}},
	})
	if err != nil {
		t.Fatalf("marshal current state: %v", err)
	}
	observation, err := core.NewProfileStateObservation("primary", state, "revision-1", now)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	input.CurrentState = observation
	input.AllowedPaths = []string{"/resumes/resume-1/web/keySkills", "/resumes/resume-1/web/skills"}
	return input
}

func aboutTailoringProcessor(t *testing.T, maximumRunes int, model ResumeTailoringAboutModel) *ModelResumeTailoringAboutProcessor {
	t.Helper()
	processor, err := NewModelResumeTailoringAboutProcessor(ModelResumeTailoringAboutConfig{
		Tag: "openai-test", PromptVersion: "v1", Instruction: "Highlight Go", MaximumRunes: maximumRunes,
		Timeout: 5 * time.Second, Model: model,
		Facts: &ApplicationResumeContext{
			ResumeID: "resume-1", FactsTag: "primary", Digest: applicationTextDigest("facts"),
			Facts: resumeTailoringAboutFacts(),
		},
	})
	if err != nil {
		t.Fatalf("new about processor: %v", err)
	}
	return processor
}

func decodeTailoredAbout(t *testing.T, plan ResumeTailoringPlan) string {
	t.Helper()
	if len(plan.Overrides) != 1 {
		t.Fatalf("overrides=%#v", plan.Overrides)
	}
	if plan.Overrides[0].Path != "/resumes/resume-1/web/skills" {
		t.Fatalf("path=%q", plan.Overrides[0].Path)
	}
	var values []string
	if err := json.Unmarshal(plan.Overrides[0].Value, &values); err != nil {
		t.Fatalf("decode about: %v", err)
	}
	if len(values) != 1 {
		t.Fatalf("values=%#v", values)
	}
	return values[0]
}

func TestModelResumeTailoringAboutAnonymizesAndSubstitutes(t *testing.T) {
	input := resumeTailoringAboutFixture(t)
	model := &fakeResumeTailoringAboutModel{response: ResumeTailoringAboutResponse{
		About: "Опытный {name} из Москвы пишет на Go.", Model: "gpt-test", ResponseID: "resp-1",
	}}
	plan, err := aboutTailoringProcessor(t, 200, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := decodeTailoredAbout(t, plan); got != "Опытный Иван из Москвы пишет на Go." {
		t.Fatalf("about=%q", got)
	}
	if plan.ProcessorTag != "openai-test" || plan.ProcessorVersion != "v1" {
		t.Fatalf("provenance tag=%q version=%q", plan.ProcessorTag, plan.ProcessorVersion)
	}
	if !validResumeTailoringDigest(plan.InputDigest) {
		t.Fatalf("input digest=%q", plan.InputDigest)
	}
	if model.request.CurrentAbout != "{name} пишет на Go" {
		t.Fatalf("model current about=%q", model.request.CurrentAbout)
	}
	if _, exists := model.request.Facts[applicationModelPlaceholdersFactKey]; exists {
		t.Fatalf("placeholders leaked into model facts: %#v", model.request.Facts)
	}
	if model.request.Facts["city"] != "Москва" || model.request.MaximumRunes != 200 {
		t.Fatalf("model request=%#v", model.request)
	}
}

func TestModelResumeTailoringAboutKeepsTextOnModelError(t *testing.T) {
	input := resumeTailoringAboutFixture(t)
	model := &fakeResumeTailoringAboutModel{err: errors.New("provider unavailable")}
	plan, err := aboutTailoringProcessor(t, 200, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Overrides) != 0 {
		t.Fatalf("overrides=%#v", plan.Overrides)
	}
	if !validResumeTailoringDigest(plan.InputDigest) {
		t.Fatalf("input digest=%q", plan.InputDigest)
	}
}

func TestModelResumeTailoringAboutRejectsUngroundedContent(t *testing.T) {
	for name, about := range map[string]string{
		"number":     "5 лет опыта и {name}",
		"email":      "Пишите на ivan@example.com",
		"undeclared": "{name} {surname} пишет на Go",
		"fence":      "```Опытный {name}```",
		"structured": `{"about": "text"}`,
	} {
		t.Run(name, func(t *testing.T) {
			input := resumeTailoringAboutFixture(t)
			model := &fakeResumeTailoringAboutModel{response: ResumeTailoringAboutResponse{About: about}}
			plan, err := aboutTailoringProcessor(t, 200, model).Plan(context.Background(), input)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if len(plan.Overrides) != 0 {
				t.Fatalf("unexpected override: %#v", plan.Overrides)
			}
		})
	}
}

func TestModelResumeTailoringAboutRejectsOversizedText(t *testing.T) {
	input := resumeTailoringAboutFixture(t)
	model := &fakeResumeTailoringAboutModel{response: ResumeTailoringAboutResponse{
		About: "Очень длинный текст про Go и PostgreSQL, который превышает лимит",
	}}
	plan, err := aboutTailoringProcessor(t, 10, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Overrides) != 0 {
		t.Fatalf("overrides=%#v", plan.Overrides)
	}
}

func TestModelResumeTailoringAboutSkipsUnchangedText(t *testing.T) {
	input := resumeTailoringAboutFixture(t)
	model := &fakeResumeTailoringAboutModel{response: ResumeTailoringAboutResponse{About: "{name} пишет на Go"}}
	plan, err := aboutTailoringProcessor(t, 200, model).Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Overrides) != 0 {
		t.Fatalf("overrides=%#v", plan.Overrides)
	}
}

func TestChainResumeTailoringMergesSkillAndAboutPaths(t *testing.T) {
	input := resumeTailoringAboutFixture(t)
	skills, err := NewAddVacancySkillsProcessor("skills-from-vacancy", "v1", 10)
	if err != nil {
		t.Fatalf("skills processor: %v", err)
	}
	model := &fakeResumeTailoringAboutModel{response: ResumeTailoringAboutResponse{About: "Опытный {name} пишет на Go."}}
	chain, err := NewChainResumeTailoringProcessor(ChainResumeTailoringConfig{
		Tag: "application-tailoring", Version: "v1", Processors: []ResumeTailoringProcessor{
			skills, aboutTailoringProcessor(t, 200, model),
		},
	})
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	plan, err := chain.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Overrides) != 2 {
		t.Fatalf("overrides=%#v", plan.Overrides)
	}
	if plan.Overrides[0].Path != "/resumes/resume-1/web/keySkills" || plan.Overrides[1].Path != "/resumes/resume-1/web/skills" {
		t.Fatalf("override paths=%#v", plan.Overrides)
	}
	if !validResumeTailoringDigest(plan.InputDigest) {
		t.Fatalf("input digest=%q", plan.InputDigest)
	}
}

func TestChainResumeTailoringRejectsDuplicatePath(t *testing.T) {
	input := resumeTailoringAboutFixture(t)
	first, err := NewAddVacancySkillsProcessor("skills-one", "v1", 10)
	if err != nil {
		t.Fatalf("first processor: %v", err)
	}
	second, err := NewAddVacancySkillsProcessor("skills-two", "v1", 10)
	if err != nil {
		t.Fatalf("second processor: %v", err)
	}
	chain, err := NewChainResumeTailoringProcessor(ChainResumeTailoringConfig{
		Tag: "application-tailoring", Version: "v1", Processors: []ResumeTailoringProcessor{first, second},
	})
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if _, err := chain.Plan(context.Background(), input); err == nil {
		t.Fatal("expected duplicate path error")
	}
}

func TestChainResumeTailoringRequiresTwoProcessors(t *testing.T) {
	processor, err := NewAddVacancySkillsProcessor("skills", "v1", 10)
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	if _, err := NewChainResumeTailoringProcessor(ChainResumeTailoringConfig{
		Tag: "application-tailoring", Version: "v1", Processors: []ResumeTailoringProcessor{processor},
	}); err == nil {
		t.Fatal("expected chain configuration error")
	}
}
