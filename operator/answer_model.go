package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Darkon13/job-agent/core"
)

const maximumAnswerModelRunes = 1500

var answerModelURLPattern = regexp.MustCompile(`https?://[^[:space:]<>()\[\]{}"]+`)
var answerModelEmailPattern = regexp.MustCompile(`[[:alnum:]._%+\-]+@[[:alnum:].\-]+\.[[:alpha:]]{2,}`)

// AnswerModelRequest is the bounded input of one question fallback. It must
// contain the question and its presented options only: no vacancy, profile,
// resume or conversation data is eligible for model answers.
type AnswerModelRequest struct {
	Platform      core.Platform     `json:"platform"`
	QuestionKind  core.QuestionKind `json:"question_kind"`
	QuestionText  string            `json:"question_text"`
	Options       []string          `json:"options,omitempty"`
	PromptVersion string            `json:"prompt_version,omitempty"`
	Instruction   string            `json:"instruction,omitempty"`
}

// AnswerModelResponse is the structured provider output before the local
// validator maps it back to presented options.
type AnswerModelResponse struct {
	SelectedOptions []string `json:"selected_options,omitempty"`
	Text            string   `json:"text,omitempty"`
	Confidence      string   `json:"confidence,omitempty"`
	Model           string   `json:"model,omitempty"`
	ResponseID      string   `json:"response_id,omitempty"`
}

// AnswerGenerator is the provider-neutral model port used by question
// fallback. It is separate from the cover-letter model request so an answer
// prompt can never inherit application context.
type AnswerGenerator interface {
	GenerateAnswer(context.Context, AnswerModelRequest) (AnswerModelResponse, error)
}

type AnswerModelConfig struct {
	Tag           string
	PromptVersion string
	Instruction   string
	Timeout       time.Duration
	Generator     AnswerGenerator
}

// AnswerModel is a compiled, profile-scoped fallback policy. It never submits
// anything: it validates the provider output and returns a portable stored
// answer with provenance. The caller decides whether to submit it.
type AnswerModel struct {
	tag           string
	promptVersion string
	instruction   string
	timeout       time.Duration
	generator     AnswerGenerator
}

func CompileAnswerModel(config *AnswerModelConfig) (*AnswerModel, error) {
	if config == nil {
		return nil, nil
	}
	model := &AnswerModel{
		tag: strings.TrimSpace(config.Tag), promptVersion: strings.TrimSpace(config.PromptVersion),
		instruction: strings.TrimSpace(config.Instruction), timeout: config.Timeout, generator: config.Generator,
	}
	if model.tag == "" || model.promptVersion == "" || model.instruction == "" || model.generator == nil {
		return nil, errors.New("answer model requires tag, prompt version, instruction and generator")
	}
	if model.timeout <= 0 {
		return nil, errors.New("answer model requires a positive timeout")
	}
	return model, nil
}

func (model *AnswerModel) Tag() string {
	if model == nil {
		return ""
	}
	return model.tag
}

// Resolve asks the provider for one answer and returns it only after the local
// validator confirmed that it refers exclusively to presented options.
func (model *AnswerModel) Resolve(ctx context.Context, platform core.Platform, question core.Question) (core.StoredAnswer, error) {
	if model == nil {
		return core.StoredAnswer{}, errors.New("answer model is nil")
	}
	request, err := NewAnswerModelRequest(platform, question, model.promptVersion)
	if err != nil {
		return core.StoredAnswer{}, err
	}
	request.Instruction = model.instruction
	encoded, err := json.Marshal(request)
	if err != nil {
		return core.StoredAnswer{}, &ModelError{Kind: ModelFailurePermanent, Operation: "answers.model", Message: "encode answer request", Cause: err}
	}
	modelCtx, cancel := context.WithTimeout(ctx, model.timeout)
	defer cancel()
	response, err := model.generator.GenerateAnswer(modelCtx, request)
	if err != nil {
		return core.StoredAnswer{}, err
	}
	if err := ValidateAnswerModelResponse(request, response); err != nil {
		return core.StoredAnswer{}, err
	}
	fingerprint, err := core.QuestionFingerprint(question)
	if err != nil {
		return core.StoredAnswer{}, &ModelError{Kind: ModelFailurePermanent, Operation: "answers.model", Message: "fingerprint question", Cause: err}
	}
	output, err := json.Marshal(struct {
		SelectedOptions []string `json:"selected_options,omitempty"`
		Text            string   `json:"text,omitempty"`
	}{SelectedOptions: response.SelectedOptions, Text: response.Text})
	if err != nil {
		return core.StoredAnswer{}, &ModelError{Kind: ModelFailurePermanent, Operation: "answers.model", Message: "encode answer output", Cause: err}
	}
	confidence := strings.TrimSpace(response.Confidence)
	if confidence == "" {
		confidence = core.AnswerConfidenceMedium
	}
	return core.StoredAnswer{
		Question: question.Text, QuestionFingerprint: fingerprint,
		SelectedOptions: append([]string(nil), response.SelectedOptions...), Text: response.Text,
		Provenance: &core.AnswerProvenance{
			Resolver: core.AnswerResolverModel, ModelTag: model.tag,
			ProviderModel: strings.TrimSpace(response.Model), PromptVersion: model.promptVersion,
			ResponseID:  strings.TrimSpace(response.ResponseID),
			InputDigest: applicationBytesDigest(encoded), OutputDigest: applicationBytesDigest(output),
			Confidence: confidence,
		},
	}, nil
}

func NewAnswerModelRequest(platform core.Platform, question core.Question, promptVersion string) (AnswerModelRequest, error) {
	if platform == "" || strings.TrimSpace(question.Text) == "" {
		return AnswerModelRequest{}, errors.New("answer model request requires platform and question text")
	}
	switch question.Kind {
	case core.QuestionSingle, core.QuestionMultiple:
		if len(question.Options) == 0 {
			return AnswerModelRequest{}, errors.New("answer model request requires presented options")
		}
	case core.QuestionText:
	case core.QuestionCode:
		return AnswerModelRequest{}, &ModelError{Kind: ModelFailurePermanent, Operation: "answers.model", Message: "code questions are not supported"}
	default:
		return AnswerModelRequest{}, fmt.Errorf("answer model request has unsupported question kind %q", question.Kind)
	}
	options := make([]string, 0, len(question.Options))
	for _, option := range question.Options {
		text := strings.TrimSpace(option.Text)
		if text == "" {
			return AnswerModelRequest{}, errors.New("answer model request contains an empty option")
		}
		options = append(options, text)
	}
	return AnswerModelRequest{
		Platform: platform, QuestionKind: question.Kind,
		QuestionText: strings.TrimSpace(question.Text), Options: options,
		PromptVersion: strings.TrimSpace(promptVersion),
	}, nil
}

// ValidateAnswerModelResponse is the deterministic local gate. A choice answer
// must match exactly one (single) or a non-empty subset (multiple) of the
// presented options; a text answer must be bounded and free of contact data.
func ValidateAnswerModelResponse(request AnswerModelRequest, response AnswerModelResponse) error {
	invalid := func(message string) error {
		return &ModelError{Kind: ModelFailureInvalidOutput, Operation: "answers.model", Message: message}
	}
	selected := make([]string, 0, len(response.SelectedOptions))
	for _, option := range response.SelectedOptions {
		selected = append(selected, strings.TrimSpace(option))
	}
	text := strings.TrimSpace(response.Text)
	switch request.QuestionKind {
	case core.QuestionSingle:
		if text != "" || len(selected) != 1 {
			return invalid("single-choice answer requires exactly one selected option")
		}
	case core.QuestionMultiple:
		if text != "" || len(selected) == 0 {
			return invalid("multiple-choice answer requires selected options")
		}
	case core.QuestionText:
		if text == "" || len(selected) != 0 {
			return invalid("text answer requires text only")
		}
		if err := validateAnswerModelText(text); err != nil {
			return err
		}
	default:
		return invalid("unsupported question kind")
	}
	if len(selected) != 0 {
		available := make(map[string]int, len(request.Options))
		for _, option := range request.Options {
			available[core.NormalizeQuestionText(option)]++
		}
		seen := make(map[string]struct{}, len(selected))
		for _, option := range selected {
			key := core.NormalizeQuestionText(option)
			if available[key] == 0 {
				return invalid("answer contains an option that was not presented")
			}
			if _, duplicate := seen[key]; duplicate {
				return invalid("answer selects one option more than once")
			}
			seen[key] = struct{}{}
		}
		if request.QuestionKind == core.QuestionSingle && len(selected) != 1 {
			return invalid("single-choice answer requires exactly one selected option")
		}
	}
	if response.Confidence != "" {
		switch response.Confidence {
		case core.AnswerConfidenceHigh, core.AnswerConfidenceMedium, core.AnswerConfidenceLow:
		default:
			return invalid("answer contains an unsupported confidence")
		}
	}
	return nil
}

func validateAnswerModelText(text string) error {
	invalid := func(message string) error {
		return &ModelError{Kind: ModelFailureInvalidOutput, Operation: "answers.model", Message: message}
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > maximumAnswerModelRunes {
		return invalid("model returned invalid or oversized answer text")
	}
	if strings.Contains(text, "```") || strings.Contains(text, "{{") || strings.Contains(text, "}}") {
		return invalid("model returned a code fence or unresolved placeholder")
	}
	if answerModelURLPattern.MatchString(text) || answerModelEmailPattern.MatchString(text) {
		return invalid("model answer must not contain contacts")
	}
	for _, runeValue := range text {
		if unicode.IsControl(runeValue) && runeValue != '\n' && runeValue != '\t' {
			return invalid("model answer contains control characters")
		}
	}
	return nil
}
