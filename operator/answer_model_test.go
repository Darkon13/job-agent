package operator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

type answerModelFunc func(context.Context, AnswerModelRequest) (AnswerModelResponse, error)

func (function answerModelFunc) GenerateAnswer(ctx context.Context, request AnswerModelRequest) (AnswerModelResponse, error) {
	return function(ctx, request)
}

func singleChoiceQuestion() core.Question {
	return core.Question{
		ID: "runtime-1", Text: "Какой оператор выбирает строки с парой?", Kind: core.QuestionSingle,
		Options: []core.QuestionOption{{ID: "a", Text: "INNER JOIN"}, {ID: "b", Text: "LEFT JOIN"}},
	}
}

func TestAnswerModelValidatesSingleChoiceAndRecordsProvenance(t *testing.T) {
	var received AnswerModelRequest
	model, err := CompileAnswerModel(&AnswerModelConfig{
		Tag: "answer-mini", PromptVersion: "v1", Instruction: "Pick one option.", Timeout: time.Second,
		Generator: answerModelFunc(func(_ context.Context, request AnswerModelRequest) (AnswerModelResponse, error) {
			received = request
			return AnswerModelResponse{
				SelectedOptions: []string{"INNER JOIN"}, Confidence: core.AnswerConfidenceHigh,
				Model: "mini-model", ResponseID: "response-1",
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	answer, err := model.Resolve(context.Background(), "hh", singleChoiceQuestion())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if answer.Text != "" || len(answer.SelectedOptions) != 1 || answer.SelectedOptions[0] != "INNER JOIN" || answer.QuestionFingerprint == "" {
		t.Fatalf("answer = %#v", answer)
	}
	provenance := answer.Provenance
	if provenance == nil || provenance.Resolver != core.AnswerResolverModel || provenance.ModelTag != "answer-mini" ||
		provenance.ProviderModel != "mini-model" || provenance.PromptVersion != "v1" || provenance.ResponseID != "response-1" ||
		provenance.Confidence != core.AnswerConfidenceHigh || provenance.Verified ||
		!strings.HasPrefix(provenance.InputDigest, "sha256:") || !strings.HasPrefix(provenance.OutputDigest, "sha256:") {
		t.Fatalf("provenance = %#v", provenance)
	}
	if received.Platform != "hh" || received.QuestionKind != core.QuestionSingle || len(received.Options) != 2 ||
		received.Options[0] != "INNER JOIN" || received.PromptVersion != "v1" || received.Instruction != "Pick one option." {
		t.Fatalf("request = %#v", received)
	}
}

func TestAnswerModelRejectsAnswersOutsidePresentedOptions(t *testing.T) {
	model, err := CompileAnswerModel(&AnswerModelConfig{
		Tag: "answer-mini", PromptVersion: "v1", Instruction: "Pick one option.", Timeout: time.Second,
		Generator: answerModelFunc(func(context.Context, AnswerModelRequest) (AnswerModelResponse, error) {
			return AnswerModelResponse{SelectedOptions: []string{"FULL JOIN"}, Confidence: core.AnswerConfidenceHigh}, nil
		}),
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, err = model.Resolve(context.Background(), "hh", singleChoiceQuestion())
	if ModelFailureKindOf(err) != ModelFailureInvalidOutput {
		t.Fatalf("error = %v, want invalid output", err)
	}
}

func TestAnswerModelValidatesMultipleAndText(t *testing.T) {
	multiple := core.Question{
		ID: "runtime-2", Text: "Какие типы конкурентны?", Kind: core.QuestionMultiple,
		Options: []core.QuestionOption{{ID: "a", Text: "goroutine"}, {ID: "b", Text: "mutex"}, {ID: "c", Text: "slice"}},
	}
	tests := []struct {
		name    string
		request AnswerModelRequest
		answer  AnswerModelResponse
		wantErr bool
	}{
		{name: "multiple subset", request: AnswerModelRequest{QuestionKind: core.QuestionMultiple, Options: []string{"goroutine", "mutex"}},
			answer: AnswerModelResponse{SelectedOptions: []string{"goroutine", "mutex"}}},
		{name: "multiple empty", request: AnswerModelRequest{QuestionKind: core.QuestionMultiple, Options: []string{"goroutine"}},
			answer: AnswerModelResponse{}, wantErr: true},
		{name: "multiple duplicate", request: AnswerModelRequest{QuestionKind: core.QuestionMultiple, Options: []string{"goroutine"}},
			answer: AnswerModelResponse{SelectedOptions: []string{"goroutine", "goroutine"}}, wantErr: true},
		{name: "multiple with text", request: AnswerModelRequest{QuestionKind: core.QuestionMultiple, Options: []string{"goroutine"}},
			answer: AnswerModelResponse{SelectedOptions: []string{"goroutine"}, Text: "extra"}, wantErr: true},
		{name: "text answer", request: AnswerModelRequest{QuestionKind: core.QuestionText},
			answer: AnswerModelResponse{Text: "Использую каналы и мьютексы."}},
		{name: "text with contact", request: AnswerModelRequest{QuestionKind: core.QuestionText},
			answer: AnswerModelResponse{Text: "Пишите на test@example.com"}, wantErr: true},
		{name: "text with placeholder", request: AnswerModelRequest{QuestionKind: core.QuestionText},
			answer: AnswerModelResponse{Text: "Вставьте {{company}}"}, wantErr: true},
		{name: "text empty", request: AnswerModelRequest{QuestionKind: core.QuestionText},
			answer: AnswerModelResponse{}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateAnswerModelResponse(test.request, test.answer)
			if test.wantErr && err == nil {
				t.Fatal("expected an invalid output error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	if _, err := NewAnswerModelRequest("hh", multiple, "v1"); err != nil {
		t.Fatalf("multiple request: %v", err)
	}
	code := core.Question{ID: "runtime-3", Text: "Напишите функцию", Kind: core.QuestionCode}
	if _, err := NewAnswerModelRequest("hh", code, "v1"); err == nil {
		t.Fatal("expected code question to be rejected")
	}
}

func TestAnswerModelTimeoutPropagates(t *testing.T) {
	model, err := CompileAnswerModel(&AnswerModelConfig{
		Tag: "answer-mini", PromptVersion: "v1", Instruction: "Pick one option.", Timeout: 5 * time.Millisecond,
		Generator: answerModelFunc(func(ctx context.Context, _ AnswerModelRequest) (AnswerModelResponse, error) {
			<-ctx.Done()
			return AnswerModelResponse{}, ctx.Err()
		}),
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, err = model.Resolve(context.Background(), "hh", singleChoiceQuestion())
	if ModelFailureKindOf(err) != ModelFailureTimeout {
		t.Fatalf("error = %v, want timeout", err)
	}
	if _, err := CompileAnswerModel(&AnswerModelConfig{Tag: "t", PromptVersion: "v1", Instruction: "i", Timeout: time.Second}); err == nil {
		t.Fatal("expected a missing generator to fail")
	}
	if _, err := CompileAnswerModel(&AnswerModelConfig{Tag: "t", PromptVersion: "v1", Instruction: "i"}); err == nil {
		t.Fatal("expected a missing timeout to fail")
	}
}
