package operator

import (
	"context"
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
		MessageTemplate: "Здравствуйте, {{.Employer}}! Откликаюсь на {{.Title}}. Навыки: {{range .KeySkills}}{{.}} {{end}}",
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
