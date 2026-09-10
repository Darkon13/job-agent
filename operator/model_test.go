package operator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type applicationModelFunc func(context.Context, ApplicationModelRequest) (ApplicationModelResponse, error)

func (function applicationModelFunc) Generate(ctx context.Context, request ApplicationModelRequest) (ApplicationModelResponse, error) {
	return function(ctx, request)
}

func modelResumeFixture() *ApplicationResumeContext {
	return &ApplicationResumeContext{
		ResumeID: "resume-1", FactsTag: "backend", Digest: "sha256:" + strings.Repeat("0", 64),
		Facts: map[string]any{"skills": []string{"Go", "SQL"}, "summary": "Backend developer"},
	}
}

func TestRuleTemplatePreparerGeneratesApplicationMessage(t *testing.T) {
	application, vacancy := operatorFixture()
	var received ApplicationModelRequest
	preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
		Resume:      modelResumeFixture(),
		MessagePool: &MessagePoolConfig{Tag: "fallback", Templates: []MessageTemplateConfig{{Tag: "one", Template: "fallback"}}},
		Model: &ApplicationModelConfig{
			Tag: "cover-letter-mini", PromptVersion: "v1", Instruction: "Keep it concise.", Timeout: time.Second,
			Generator: applicationModelFunc(func(_ context.Context, request ApplicationModelRequest) (ApplicationModelResponse, error) {
				received = request
				return ApplicationModelResponse{Text: "  Generated for Example  ", Model: "mini-model", ResponseID: "response-1"}, nil
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
	if result.Message != "Generated for Example" || !strings.Contains(result.Reason, `model "cover-letter-mini" generated with "mini-model" prompt "v1"`) {
		t.Fatalf("preparation = %#v", result)
	}
	if received.Context.Vacancy.ExternalID != vacancy.ExternalID || received.Context.Resume == nil ||
		received.Context.Resume.ResumeID != "resume-1" || received.PromptVersion != "v1" ||
		!strings.Contains(received.Instruction, "untrusted data") || !strings.Contains(received.Instruction, "Keep it concise") {
		t.Fatalf("model request = %#v", received)
	}
}

func TestRuleTemplatePreparerFallsBackAfterModelTimeoutOrInvalidOutput(t *testing.T) {
	application, vacancy := operatorFixture()
	tests := []struct {
		name      string
		generator applicationModelFunc
		wantKind  ModelFailureKind
	}{
		{
			name: "timeout", wantKind: ModelFailureTimeout,
			generator: func(ctx context.Context, _ ApplicationModelRequest) (ApplicationModelResponse, error) {
				<-ctx.Done()
				return ApplicationModelResponse{}, ctx.Err()
			},
		},
		{
			name: "empty", wantKind: ModelFailureInvalidOutput,
			generator: func(context.Context, ApplicationModelRequest) (ApplicationModelResponse, error) {
				return ApplicationModelResponse{Text: "  "}, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
				Resume:      modelResumeFixture(),
				MessagePool: &MessagePoolConfig{Tag: "fallback", Templates: []MessageTemplateConfig{{Tag: "safe", Template: "Safe {{.Vacancy.Title}}"}}},
				Model: &ApplicationModelConfig{
					Tag: "model", PromptVersion: "v1", Instruction: "Concise", Timeout: 5 * time.Millisecond, Generator: test.generator,
				},
			})
			if err != nil {
				t.Fatalf("new preparer: %v", err)
			}
			result, err := preparer.PrepareApplication(context.Background(), application, vacancy)
			if err != nil || result.Message != "Safe Backend developer" ||
				!strings.Contains(result.Reason, "fallback after "+string(test.wantKind)) ||
				!strings.Contains(result.Reason, `message pool "fallback" selected template "safe"`) {
				t.Fatalf("preparation = %#v, err=%v", result, err)
			}
		})
	}
}

func TestRuleTemplatePreparerDoesNotFallbackAfterParentCancellation(t *testing.T) {
	application, vacancy := operatorFixture()
	ctx, cancel := context.WithCancel(context.Background())
	preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
		StaticMessage: "fallback",
		Resume:        modelResumeFixture(),
		Model: &ApplicationModelConfig{
			Tag: "model", PromptVersion: "v1", Instruction: "Concise", Timeout: time.Second,
			Generator: applicationModelFunc(func(context.Context, ApplicationModelRequest) (ApplicationModelResponse, error) {
				cancel()
				return ApplicationModelResponse{}, context.Canceled
			}),
		},
	})
	if err != nil {
		t.Fatalf("new preparer: %v", err)
	}
	if _, err := preparer.PrepareApplication(ctx, application, vacancy); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}

func TestRuleTemplatePreparerRoutesEmployerToModelWithOwnFallback(t *testing.T) {
	application, vacancy := operatorFixture()
	matcher, err := NewEmployerGroupMatcher([]EmployerGroupConfig{{Tag: "example", Rules: []EmployerGroupRuleConfig{{Name: "Example"}}}})
	if err != nil {
		t.Fatalf("new matcher: %v", err)
	}
	preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
		StaticMessage:   "default",
		Resume:          modelResumeFixture(),
		EmployerMatcher: matcher,
		EmployerRules: []EmployerRuleConfig{{
			EmployerGroups: []string{"example"}, Action: EmployerRuleModel,
			MessagePool: &MessagePoolConfig{Tag: "employer-fallback", Templates: []MessageTemplateConfig{{Tag: "one", Template: "employer fallback"}}},
			Model: &ApplicationModelConfig{
				Tag: "employer-model", PromptVersion: "v2", Instruction: "Company specific", Timeout: time.Second,
				Generator: applicationModelFunc(func(context.Context, ApplicationModelRequest) (ApplicationModelResponse, error) {
					return ApplicationModelResponse{}, &ModelError{Kind: ModelFailureRateLimited, Operation: "responses.create"}
				}),
			},
		}},
	})
	if err != nil {
		t.Fatalf("new preparer: %v", err)
	}
	result, err := preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil || result.Message != "employer fallback" ||
		!strings.Contains(result.Reason, `matched group "example"`) ||
		!strings.Contains(result.Reason, `model "employer-model" fallback after rate_limited`) {
		t.Fatalf("preparation = %#v, err=%v", result, err)
	}
}

func TestRuleTemplatePreparerRequiresModelFallbackAndCompleteConfig(t *testing.T) {
	generator := applicationModelFunc(func(context.Context, ApplicationModelRequest) (ApplicationModelResponse, error) {
		return ApplicationModelResponse{Text: "text"}, nil
	})
	if _, err := NewRuleTemplatePreparer(RuleTemplateConfig{Model: &ApplicationModelConfig{
		Tag: "model", PromptVersion: "v1", Instruction: "Concise", Timeout: time.Second, Generator: generator,
	}, Resume: modelResumeFixture()}); err == nil {
		t.Fatal("expected missing model fallback to fail")
	}
	if _, err := NewRuleTemplatePreparer(RuleTemplateConfig{StaticMessage: "fallback", Model: &ApplicationModelConfig{
		Tag: "model", PromptVersion: "", Instruction: "Concise", Timeout: time.Second, Generator: generator,
	}, Resume: modelResumeFixture()}); err == nil {
		t.Fatal("expected incomplete model config to fail")
	}
	if _, err := NewRuleTemplatePreparer(RuleTemplateConfig{StaticMessage: "fallback", Model: &ApplicationModelConfig{
		Tag: "model", PromptVersion: "v1", Instruction: "Concise", Timeout: time.Second, Generator: generator,
	}}); err == nil {
		t.Fatal("expected missing explicit resume facts to fail")
	}
}

func TestRuleTemplatePreparerRejectsUnsafeOrUngroundedModelText(t *testing.T) {
	application, vacancy := operatorFixture()
	tests := []struct {
		name string
		text string
	}{
		{name: "unresolved placeholder", text: "Здравствуйте, {{.Resume.Name}}"},
		{name: "fenced response", text: "```text\nЗдравствуйте\n```"},
		{name: "structured response", text: `{"text":"Здравствуйте"}`},
		{name: "invented experience", text: "У меня 7 лет коммерческого опыта."},
		{name: "structural id is not evidence", text: "У меня 42 года коммерческого опыта."},
		{name: "invented contact", text: "Портфолио: https://example.invalid/portfolio"},
		{name: "control character", text: "Здравствуйте\x00"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
				StaticMessage: "safe fallback",
				Resume:        modelResumeFixture(),
				Model: &ApplicationModelConfig{
					Tag: "model", PromptVersion: "v1", Instruction: "Concise", Timeout: time.Second,
					Generator: applicationModelFunc(func(context.Context, ApplicationModelRequest) (ApplicationModelResponse, error) {
						return ApplicationModelResponse{Text: test.text}, nil
					}),
				},
			})
			if err != nil {
				t.Fatalf("new preparer: %v", err)
			}
			result, err := preparer.PrepareApplication(context.Background(), application, vacancy)
			if err != nil || result.Message != "safe fallback" || !strings.Contains(result.Reason, "fallback after invalid_output") {
				t.Fatalf("preparation = %#v, err=%v", result, err)
			}
		})
	}
}

func TestRuleTemplatePreparerAllowsGroundedNumbersAndURLs(t *testing.T) {
	application, vacancy := operatorFixture()
	vacancy.Attributes["experience"] = "От 1 года до 3 лет"
	message := "Подхожу под требование опыта от 1 года до 3 лет: https://hh.ru/vacancy/42"
	preparer, err := NewRuleTemplatePreparer(RuleTemplateConfig{
		StaticMessage: "fallback",
		Resume:        modelResumeFixture(),
		Model: &ApplicationModelConfig{
			Tag: "model", PromptVersion: "v1", Instruction: "Concise", Timeout: time.Second,
			Generator: applicationModelFunc(func(context.Context, ApplicationModelRequest) (ApplicationModelResponse, error) {
				return ApplicationModelResponse{Text: message}, nil
			}),
		},
	})
	if err != nil {
		t.Fatalf("new preparer: %v", err)
	}
	result, err := preparer.PrepareApplication(context.Background(), application, vacancy)
	if err != nil || result.Message != message || strings.Contains(result.Reason, "fallback") {
		t.Fatalf("preparation = %#v, err=%v", result, err)
	}
}
