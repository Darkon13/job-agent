package operator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

type fakeResumeTailoringExperienceModel struct {
	response ResumeTailoringExperienceResponse
	err      error
	calls    int
	request  ResumeTailoringExperienceRequest
}

func (model *fakeResumeTailoringExperienceModel) RewriteExperience(_ context.Context, request ResumeTailoringExperienceRequest) (ResumeTailoringExperienceResponse, error) {
	model.calls++
	model.request = request
	if model.err != nil {
		return ResumeTailoringExperienceResponse{}, model.err
	}
	return model.response, nil
}

func resumeTailoringExperienceFixture(t *testing.T) ResumeTailoringInput {
	t.Helper()
	input := resumeTailoringFixture(t, []string{"Go"}, []string{"PostgreSQL"})
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	state, err := json.Marshal(map[string]any{
		"resumes": map[string]any{"resume-1": map[string]any{
			"web": map[string]any{"title": "Go developer", "keySkills": []string{"Go"}},
			"web_profile": map[string]any{"experience": []any{
				map[string]any{"id": "job-1", "position": "Backend developer", "companyName": "Example", "description": "Go и PostgreSQL, 3 года"},
				map[string]any{"id": "job-2", "position": "Разработчик", "companyName": "Romashka", "description": "Поддерживал внутренние сервисы"},
			}},
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
	input.AllowedPaths = []string{ResumeExperiencePath("resume-1")}
	return input
}

func experienceTailoringProcessor(t *testing.T, model ResumeTailoringExperienceModel) *ModelResumeTailoringExperienceProcessor {
	t.Helper()
	processor, err := NewModelResumeTailoringExperienceProcessor(ModelResumeTailoringExperienceConfig{
		Tag: "deepseek-test", PromptVersion: "v1", Instruction: "Emphasize Go",
		MaximumRunes: 600, Timeout: 5 * time.Second, Model: model,
	})
	if err != nil {
		t.Fatalf("new experience processor: %v", err)
	}
	return processor
}

func TestModelResumeTailoringExperienceRewritesGroundedDescriptions(t *testing.T) {
	model := &fakeResumeTailoringExperienceModel{response: ResumeTailoringExperienceResponse{
		Entries: []ResumeTailoringExperienceEntry{{ID: "job-1", Description: "Развивал сервисы на Go и PostgreSQL, 3 года"}},
	}}
	processor := experienceTailoringProcessor(t, model)
	input := resumeTailoringExperienceFixture(t)
	plan, err := processor.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Overrides) != 1 || plan.Overrides[0].Path != ResumeExperiencePath("resume-1") {
		t.Fatalf("overrides=%#v", plan.Overrides)
	}
	var entries []map[string]any
	if err := json.Unmarshal(plan.Overrides[0].Value, &entries); err != nil {
		t.Fatalf("decode experience: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%#v", entries)
	}
	if entries[0]["description"] != "Развивал сервисы на Go и PostgreSQL, 3 года" ||
		entries[0]["position"] != "Backend developer" || entries[0]["companyName"] != "Example" {
		t.Fatalf("rewritten entry=%#v", entries[0])
	}
	if entries[1]["description"] != "Поддерживал внутренние сервисы" {
		t.Fatalf("untouched entry=%#v", entries[1])
	}
	if len(model.request.Entries) != 2 || model.request.Entries[0].ID != "job-1" || model.request.MaximumRunes != 600 {
		t.Fatalf("model request=%#v", model.request)
	}
}

func TestModelResumeTailoringExperienceKeepsCurrentOnInvalidOutput(t *testing.T) {
	cases := map[string]*fakeResumeTailoringExperienceModel{
		"ungrounded number": {response: ResumeTailoringExperienceResponse{
			Entries: []ResumeTailoringExperienceEntry{{ID: "job-1", Description: "Ускорил сервисы на 50%"}},
		}},
		"unknown id": {response: ResumeTailoringExperienceResponse{
			Entries: []ResumeTailoringExperienceEntry{{ID: "missing", Description: "Развивал сервисы на Go"}},
		}},
		"empty description": {response: ResumeTailoringExperienceResponse{
			Entries: []ResumeTailoringExperienceEntry{{ID: "job-1", Description: "  "}},
		}},
		"model failure": {err: errors.New("provider unavailable")},
	}
	for name, model := range cases {
		t.Run(name, func(t *testing.T) {
			processor := experienceTailoringProcessor(t, model)
			input := resumeTailoringExperienceFixture(t)
			plan, err := processor.Plan(context.Background(), input)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if len(plan.Overrides) != 0 {
				t.Fatalf("expected the current resume to be kept: %#v", plan.Overrides)
			}
			if plan.ProcessorTag == "" || plan.InputDigest == "" {
				t.Fatalf("plan lost provenance: %#v", plan)
			}
		})
	}
}

func TestModelResumeTailoringExperienceHonoursDeclaredTargetsAndOrder(t *testing.T) {
	model := &fakeResumeTailoringExperienceModel{response: ResumeTailoringExperienceResponse{
		Entries: []ResumeTailoringExperienceEntry{
			{ID: "job-1", Description: "Развивал сервисы на Go и PostgreSQL, 3 года"},
		},
		Order: []string{"job-2", "job-1"},
	}}
	processor, err := NewModelResumeTailoringExperienceProcessor(ModelResumeTailoringExperienceConfig{
		Tag: "deepseek-test", PromptVersion: "v1", Instruction: "Emphasize Go", MaximumRunes: 600,
		Timeout: 5 * time.Second, Model: model, AllowReorder: true,
		Targets:        []ResumeTailoringObjectReference{{Name: "experience", EntryID: "job-1", Field: "description"}},
		ContextObjects: []ResumeTailoringObjectReference{{Name: "experience", EntryID: "job-2"}},
	})
	if err != nil {
		t.Fatalf("new processor: %v", err)
	}
	input := resumeTailoringExperienceFixture(t)
	plan, err := processor.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(model.request.Entries) != 1 || model.request.Entries[0].ID != "job-1" {
		t.Fatalf("targets did not restrict the model entries: %#v", model.request.Entries)
	}
	if _, exists := model.request.ResumeContext["experience.job-2"]; !exists {
		t.Fatalf("declared context object was not sent: %#v", model.request.ResumeContext)
	}
	var entries []map[string]any
	if err := json.Unmarshal(plan.Overrides[0].Value, &entries); err != nil {
		t.Fatalf("decode experience: %v", err)
	}
	if len(entries) != 2 || entries[0]["id"] != "job-2" || entries[1]["id"] != "job-1" {
		t.Fatalf("reordered entries=%#v", entries)
	}
	if entries[1]["description"] != "Развивал сервисы на Go и PostgreSQL, 3 года" {
		t.Fatalf("description was not rewritten after reorder: %#v", entries[1])
	}
}

func TestModelResumeTailoringExperienceIgnoresOrderWhenNotDeclared(t *testing.T) {
	model := &fakeResumeTailoringExperienceModel{response: ResumeTailoringExperienceResponse{
		Order: []string{"job-2", "job-1"},
	}}
	processor := experienceTailoringProcessor(t, model)
	input := resumeTailoringExperienceFixture(t)
	plan, err := processor.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Overrides) != 0 {
		t.Fatalf("order was applied without permission: %#v", plan.Overrides)
	}
}
