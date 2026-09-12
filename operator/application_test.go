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

func TestRuleTemplatePreparerRendersProfileContacts(t *testing.T) {
	application, vacancy := operatorFixture()
	preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
		MessageTemplate: "Telegram: {{.Profile.Telegram}}\nEmail: {{.Profile.Email}}\n\nЗдравствуйте!",
		Profile: ApplicationProfileContext{
			Email: "user@example.test", Telegram: "@qworteex",
		},
	})
	if err != nil {
		t.Fatalf("new preparer: %v", err)
	}
	result, err := preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.Message != "Telegram: @qworteex\nEmail: user@example.test\n\nЗдравствуйте!" {
		t.Fatalf("message = %q", result.Message)
	}
}

func TestRuleTemplatePreparerAnonymizesProfileContacts(t *testing.T) {
	application, vacancy := operatorFixture()
	resume := modelResumeFixture()
	var received ApplicationModelRequest
	preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
		StaticMessage: "fallback",
		Resume:        resume,
		Profile: ApplicationProfileContext{
			FirstName: "Артём", LastName: "Шумилов", Email: "user@example.test", Telegram: "@qworteex",
		},
		Model: &ApplicationModelConfig{
			Tag: "model", PromptVersion: "v1", Instruction: "Concise", Timeout: time.Second,
			Generator: applicationModelFunc(func(_ context.Context, request ApplicationModelRequest) (ApplicationModelResponse, error) {
				received = request
				contactClaim := "Telegram: {telegram}\nEmail: {email}"
				greetingClaim := "Здравствуйте! Меня заинтересовала вакансия Backend developer."
				text := contactClaim + "\n\n" + greetingClaim
				return ApplicationModelResponse{
					Text: text,
					Evidence: []ApplicationModelEvidence{
						{Claim: contactClaim, Sources: []ApplicationModelEvidenceSource{
							{Path: "/profile/telegram", Quote: "{telegram}"},
							{Path: "/profile/email", Quote: "{email}"},
						}},
						{Claim: greetingClaim, Sources: []ApplicationModelEvidenceSource{
							{Path: "/vacancy/title", Quote: "Backend developer"},
						}},
					},
				}, nil
			}),
		},
	})
	if err != nil {
		t.Fatalf("new preparer: %v", err)
	}
	result, err := preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.Message != "Telegram: @qworteex\nEmail: user@example.test\n\nЗдравствуйте! Меня заинтересовала вакансия Backend developer." {
		t.Fatalf("message = %q", result.Message)
	}
	encoded, err := json.Marshal(received.Context)
	if err != nil {
		t.Fatalf("encode context: %v", err)
	}
	contextText := string(encoded)
	for _, secret := range []string{"@qworteex", "user@example.test", "Артём", "Шумилов"} {
		if strings.Contains(contextText, secret) {
			t.Fatalf("model context leaked %q: %s", secret, contextText)
		}
	}
	for _, placeholder := range []string{"{telegram}", "{email}", "{first_name}", "{last_name}"} {
		if !strings.Contains(contextText, placeholder) {
			t.Fatalf("model context is missing %s: %s", placeholder, contextText)
		}
	}
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
	if result.Outcome != ApplicationApply || result.Code != "qualified" || !strings.Contains(result.Message, "Example") || !strings.Contains(result.Message, "Go SQL") ||
		result.Provenance.Source != core.ApplicationPreparationSourceTemplate || result.Provenance.TemplateTag != "inline" {
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

func TestRuleTemplatePreparerRendersExplicitResumeFacts(t *testing.T) {
	application, vacancy := operatorFixture()
	resume := modelResumeFixture()
	preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
		Resume:          resume,
		MessageTemplate: `{{index .Resume.Facts "summary"}}: {{.Vacancy.Title}}`,
	})
	if err != nil {
		t.Fatalf("new preparer: %v", err)
	}
	resume.Facts["summary"] = "mutated after composition"
	result, err := preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil || result.Message != "Backend developer: Backend developer" {
		t.Fatalf("preparation=%#v err=%v", result, err)
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
	if !strings.HasPrefix(first.Provenance.MessagePoolDigest, "sha256:") ||
		first.Provenance.MessagePoolDigest != second.Provenance.MessagePoolDigest {
		t.Fatalf("message pool digest is missing or unstable: first=%#v second=%#v", first.Provenance, second.Provenance)
	}
}

func TestMessagePoolDigestTracksNormalizedContent(t *testing.T) {
	base := &MessagePoolConfig{Tag: "backend", Templates: []MessageTemplateConfig{{Tag: "one", Template: "Hello"}}}
	first, err := compileMessagePool(base, nil, ApplicationProfileContext{})
	if err != nil {
		t.Fatalf("compile first pool: %v", err)
	}
	same, err := compileMessagePool(&MessagePoolConfig{
		Tag: " backend ", Strategy: MessagePoolFirst,
		Templates: []MessageTemplateConfig{{Tag: " one ", Template: "Hello"}},
	}, nil, ApplicationProfileContext{})
	if err != nil {
		t.Fatalf("compile equivalent pool: %v", err)
	}
	changed, err := compileMessagePool(&MessagePoolConfig{
		Tag: "backend", Templates: []MessageTemplateConfig{{Tag: "one", Template: "Hello!"}},
	}, nil, ApplicationProfileContext{})
	if err != nil {
		t.Fatalf("compile changed pool: %v", err)
	}
	if first.digest != same.digest || first.digest == changed.digest {
		t.Fatalf("unexpected content digests: first=%q same=%q changed=%q", first.digest, same.digest, changed.digest)
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

func TestRuleTemplatePreparerRoutesMessagePoolByEmployerGroup(t *testing.T) {
	application, vacancy := operatorFixture()
	vacancy.Employer = "Ozon Tech"
	vacancy.Attributes["employer_id"] = "2180"
	matcher, err := NewEmployerGroupMatcher([]EmployerGroupConfig{
		{Tag: "ozon", Rules: []EmployerGroupRuleConfig{{Platform: "hh", EmployerID: "2180"}}},
		{Tag: "marketplaces", Include: []string{"ozon"}},
	})
	if err != nil {
		t.Fatalf("new employer matcher: %v", err)
	}
	preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
		MessagePool:     &MessagePoolConfig{Tag: "default", Templates: []MessageTemplateConfig{{Tag: "default", Template: "Обычное письмо"}}},
		EmployerMatcher: matcher,
		EmployerRules: []EmployerRuleConfig{{
			EmployerGroups: []string{"marketplaces"}, Action: EmployerRuleMessagePool,
			MessagePool: &MessagePoolConfig{Tag: "marketplace", Templates: []MessageTemplateConfig{{Tag: "focused", Template: "Письмо для {{.Vacancy.Employer}}"}}},
		}},
	})
	if err != nil {
		t.Fatalf("new preparer: %v", err)
	}
	result, err := preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.Message != "Письмо для Ozon Tech" ||
		!strings.Contains(result.Reason, `matched group "marketplaces" by included_group via "ozon"`) ||
		!strings.Contains(result.Reason, `message pool "marketplace" selected template "focused"`) {
		t.Fatalf("preparation = %#v", result)
	}
}

func TestRuleTemplatePreparerEmployerRulesAreOrderedAndCanStopApplication(t *testing.T) {
	application, vacancy := operatorFixture()
	matcher, err := NewEmployerGroupMatcher([]EmployerGroupConfig{
		{Tag: "example", Rules: []EmployerGroupRuleConfig{{Name: "Example"}}},
	})
	if err != nil {
		t.Fatalf("new employer matcher: %v", err)
	}
	for _, test := range []struct {
		name    string
		action  string
		outcome ApplicationOutcome
		code    string
	}{
		{name: "skip", action: EmployerRuleSkip, outcome: ApplicationSkip, code: "employer_rule_skip"},
		{name: "review", action: EmployerRuleReview, outcome: ApplicationReview, code: "employer_rule_review"},
	} {
		t.Run(test.name, func(t *testing.T) {
			preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
				EmployerMatcher: matcher,
				EmployerRules: []EmployerRuleConfig{
					{EmployerGroups: []string{"example"}, Action: test.action},
					{EmployerGroups: []string{"example"}, Action: EmployerRuleMessagePool, MessagePool: &MessagePoolConfig{
						Tag: "must-not-win", Templates: []MessageTemplateConfig{{Tag: "one", Template: "unexpected"}},
					}},
				},
			})
			if err != nil {
				t.Fatalf("new preparer: %v", err)
			}
			result, err := preparer.PrepareApplication(context.Background(), application, vacancy)
			if err != nil || result.Outcome != test.outcome || result.Code != test.code || result.Message != "" {
				t.Fatalf("preparation = %#v, err=%v", result, err)
			}
		})
	}
}

func TestRuleTemplatePreparerValidatesEmployerRules(t *testing.T) {
	matcher, err := NewEmployerGroupMatcher([]EmployerGroupConfig{{Tag: "known", Rules: []EmployerGroupRuleConfig{{Name: "Example"}}}})
	if err != nil {
		t.Fatalf("new employer matcher: %v", err)
	}
	tests := []struct {
		name   string
		config RuleTemplateConfig
	}{
		{name: "missing matcher", config: RuleTemplateConfig{EmployerRules: []EmployerRuleConfig{{EmployerGroups: []string{"known"}, Action: EmployerRuleSkip}}}},
		{name: "unknown group", config: RuleTemplateConfig{EmployerMatcher: matcher, EmployerRules: []EmployerRuleConfig{{EmployerGroups: []string{"missing"}, Action: EmployerRuleSkip}}}},
		{name: "missing pool", config: RuleTemplateConfig{EmployerMatcher: matcher, EmployerRules: []EmployerRuleConfig{{EmployerGroups: []string{"known"}, Action: EmployerRuleMessagePool}}}},
		{name: "pool on skip", config: RuleTemplateConfig{EmployerMatcher: matcher, EmployerRules: []EmployerRuleConfig{{EmployerGroups: []string{"known"}, Action: EmployerRuleSkip, MessagePool: &MessagePoolConfig{Tag: "pool", Templates: []MessageTemplateConfig{{Tag: "one", Template: "text"}}}}}}},
		{name: "unknown action", config: RuleTemplateConfig{EmployerMatcher: matcher, EmployerRules: []EmployerRuleConfig{{EmployerGroups: []string{"known"}, Action: "route"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRuleTemplatePreparer(test.config); err == nil {
				t.Fatal("expected invalid employer rule to fail")
			}
		})
	}
}
