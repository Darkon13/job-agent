package operator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func operatorFixture() (core.Application, core.Vacancy) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	application, _ := core.NewApplication("application-1", core.ApplicationKey{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	}, now)
	vacancy := core.Vacancy{
		Platform: "hh", ExternalID: "42", URL: "https://hh.ru/vacancy/42",
		Title: "Backend developer", Employer: "Example", State: core.VacancyStateOpen, ObservedAt: now,
		Attributes: map[string]any{
			"description": "<p>Разработка сервисов на Go и PostgreSQL</p>",
			"key_skills":  []string{"Go", "SQL"},
		},
	}
	return application, vacancy
}

func TestRuleTemplatePreparerFiltersAndRenders(t *testing.T) {
	application, vacancy := operatorFixture()
	preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
		IncludeAny: []string{"golang", "Go"}, ExcludeAny: []string{"team lead"},
		MessageTemplate: "Здравствуйте, {{.Vacancy.Employer}}! Откликаюсь на {{.Vacancy.Title}}. Навыки: {{range .Vacancy.KeySkills}}{{.}} {{end}}",
	})
	if err != nil {
		t.Fatalf("new preparer: %v", err)
	}
	result, err := preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.Outcome != ApplicationApply || result.Code != "qualified" || !strings.Contains(result.Message, "Example") || !strings.Contains(result.Message, "Go SQL") {
		t.Fatalf("preparation = %#v", result)
	}

	vacancy.Attributes["description"] = "<p>Ищем Team Lead</p>"
	result, err = preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil || result.Outcome != ApplicationSkip || result.Code != "excluded_term" || result.Message != "" {
		t.Fatalf("excluded preparation = %#v err=%v", result, err)
	}
}

func TestApplicationTemplateDataIncludesStructuredVacancyContext(t *testing.T) {
	application, vacancy := operatorFixture()
	vacancy.Attributes["experience"] = map[string]any{"id": "between1And3", "name": "От 1 года до 3 лет"}
	data := NewApplicationTemplateData(application, vacancy)
	if data.ApplicationID != "application-1" || data.ProfileID != "primary" || data.Vacancy.ExternalID != "42" ||
		data.Vacancy.Description == "" || len(data.Vacancy.KeySkills) != 2 || data.Vacancy.Attributes["experience"] == nil {
		t.Fatalf("template data=%#v", data)
	}
	delete(data.Vacancy.Attributes, "description")
	if vacancy.Attributes["description"] == nil {
		t.Fatal("template context mutated source vacancy attributes")
	}
	data.Vacancy.Attributes["experience"].(map[string]any)["name"] = "changed"
	if vacancy.Attributes["experience"].(map[string]any)["name"] == "changed" {
		t.Fatal("template context retained nested source attribute map")
	}
	encoded, err := json.Marshal(data)
	if err != nil || !strings.Contains(string(encoded), `"vacancy":{"platform":"hh"`) || strings.Contains(string(encoded), `"Title"`) {
		t.Fatalf("serialized context=%s err=%v", encoded, err)
	}
}

func TestRuleTemplatePreparerRequiresOneIncludedTerm(t *testing.T) {
	application, vacancy := operatorFixture()
	preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{IncludeAny: []string{"Rust"}})
	if err != nil {
		t.Fatalf("new preparer: %v", err)
	}
	result, err := preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil || result.Outcome != ApplicationSkip || result.Code != "include_term_missing" {
		t.Fatalf("preparation = %#v err=%v", result, err)
	}
	vacancy.Title = "Django developer"
	vacancy.Attributes = map[string]any{"description": "Python web framework", "key_skills": []string{"Python"}}
	preparer, err = NewRuleTemplatePreparer(RuleTemplateConfig{IncludeAny: []string{"Go"}})
	if err != nil {
		t.Fatalf("new boundary preparer: %v", err)
	}
	result, err = preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil || result.Outcome != ApplicationSkip {
		t.Fatalf("term Go must not match Django: preparation=%#v err=%v", result, err)
	}
}

func TestRuleTemplatePreparerRejectsAmbiguousOrOversizedMessage(t *testing.T) {
	if _, err := NewRuleTemplatePreparer(RuleTemplateConfig{StaticMessage: "one", MessageTemplate: "two"}); err == nil {
		t.Fatal("expected ambiguous message config to fail")
	}
	if _, err := NewRuleTemplatePreparer(RuleTemplateConfig{StaticMessage: strings.Repeat("я", maximumApplicationMessageRunes+1)}); err == nil {
		t.Fatal("expected oversized message to fail")
	}
	if _, err := NewRuleTemplatePreparer(RuleTemplateConfig{MessageTemplate: "{{.Missing}}"}); err == nil {
		t.Fatal("expected unknown template field to fail during parsing")
	}
}

func TestRuleTemplatePreparerSelectsStableMessagePoolVariant(t *testing.T) {
	application, vacancy := operatorFixture()
	preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{MessagePool: &MessagePoolConfig{
		Tag: "backend", Strategy: MessagePoolStableHash,
		Templates: []MessageTemplateConfig{
			{Tag: "concise", Template: "Коротко: {{.Vacancy.Title}}"},
			{Tag: "detailed", Template: "Подробно: {{.Vacancy.Title}} в {{.Vacancy.Employer}}"},
		},
	}})
	if err != nil {
		t.Fatalf("new pool preparer: %v", err)
	}
	first, err := preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	second, err := preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	if first.Message != second.Message || !strings.Contains(first.Reason, `message pool "backend" selected template`) {
		t.Fatalf("unstable pool result: first=%#v second=%#v", first, second)
	}
}

func TestRuleTemplatePreparerValidatesMessagePool(t *testing.T) {
	_, err := NewRuleTemplatePreparer(RuleTemplateConfig{MessagePool: &MessagePoolConfig{
		Tag: "backend", Strategy: "random", Templates: []MessageTemplateConfig{{Tag: "one", Template: "Hello"}},
	}})
	if err == nil {
		t.Fatal("expected unsupported pool strategy to fail")
	}
	_, err = NewRuleTemplatePreparer(RuleTemplateConfig{MessagePool: &MessagePoolConfig{
		Tag: "backend", Templates: []MessageTemplateConfig{{Tag: "one", Template: "Hello"}, {Tag: "one", Template: "Again"}},
	}})
	if err == nil {
		t.Fatal("expected duplicate pool template tag to fail")
	}
}
