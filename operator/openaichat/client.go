package openaichat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	applicationoperator "github.com/Darkon13/job-agent/operator"
)

// Client talks to any OpenAI-compatible chat completions endpoint such as
// DeepSeek or a local gateway. Structured output uses JSON mode plus a strict
// local decoder, so providers without json_schema support can still serve the
// same operator ports as the Responses client.
const (
	DefaultBaseURL         = "https://api.deepseek.com/v1"
	defaultMaxOutputTokens = 1024
	maximumMaxOutputTokens = 32768
	maximumRequestBytes    = 512 << 10
	maximumResponseBytes   = 1 << 20
)

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Config struct {
	BaseURL         string
	APIKey          string
	Model           string
	MaxOutputTokens int
	ReasoningEffort string
	HTTPClient      HTTPClient
}

type Client struct {
	endpoint        string
	apiKey          string
	model           string
	maxOutputTokens int
	reasoningEffort string
	httpClient      HTTPClient
}

var _ applicationoperator.ApplicationMessageModel = (*Client)(nil)
var _ applicationoperator.AnswerGenerator = (*Client)(nil)
var _ applicationoperator.ResumeTailoringModel = (*Client)(nil)
var _ applicationoperator.ResumeTailoringAboutModel = (*Client)(nil)

func New(config Config) (*Client, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("chat completions base URL must be an absolute URL without credentials, query or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopbackHost(parsed.Hostname())) {
		return nil, errors.New("chat completions base URL must use HTTPS or loopback HTTP")
	}
	apiKey := strings.TrimSpace(config.APIKey)
	model := strings.TrimSpace(config.Model)
	if apiKey == "" || model == "" {
		return nil, errors.New("chat completions client requires API key and model")
	}
	maxOutputTokens := config.MaxOutputTokens
	if maxOutputTokens == 0 {
		maxOutputTokens = defaultMaxOutputTokens
	}
	if maxOutputTokens < 1 || maxOutputTokens > maximumMaxOutputTokens {
		return nil, fmt.Errorf("chat completions max output tokens must be between 1 and %d", maximumMaxOutputTokens)
	}
	endpoint, err := url.JoinPath(baseURL, "chat/completions")
	if err != nil {
		return nil, fmt.Errorf("build chat completions endpoint: %w", err)
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	reasoningEffort := strings.TrimSpace(config.ReasoningEffort)
	switch reasoningEffort {
	case "", "none", "low", "medium", "high":
	default:
		return nil, errors.New("chat completions reasoning effort must be none, low, medium or high")
	}
	return &Client{
		endpoint: endpoint, apiKey: apiKey, model: model,
		maxOutputTokens: maxOutputTokens, reasoningEffort: reasoningEffort, httpClient: httpClient,
	}, nil
}

func (client *Client) Generate(ctx context.Context, request applicationoperator.ApplicationModelRequest) (applicationoperator.ApplicationModelResponse, error) {
	if client == nil || client.httpClient == nil {
		return applicationoperator.ApplicationModelResponse{}, errors.New("chat completions client is nil")
	}
	if strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.PromptVersion) == "" {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "instruction and prompt version are required",
		}
	}
	contextJSON, err := json.Marshal(request.Context)
	if err != nil {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "encode application context", Cause: err,
		}
	}
	if len(contextJSON) > maximumRequestBytes {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "application context is too large",
		}
	}
	content, meta, err := client.createCompletion(ctx, "chat.completions", request.Instruction,
		"Generate a cover letter from this structured context JSON:\n"+string(contextJSON),
		`{"text": string, "evidence": [{"claim": string, "sources": [{"path": string, "quote": string}]}]}`)
	if err != nil {
		return applicationoperator.ApplicationModelResponse{}, err
	}
	var result struct {
		Text     string                                         `json:"text"`
		Evidence []applicationoperator.ApplicationModelEvidence `json:"evidence"`
	}
	if err := decodeStructured(content, &result); err != nil {
		return applicationoperator.ApplicationModelResponse{}, err
	}
	return applicationoperator.ApplicationModelResponse{
		Text: result.Text, Evidence: result.Evidence, Model: meta.model, ResponseID: meta.responseID,
	}, nil
}

func (client *Client) GenerateAnswer(ctx context.Context, request applicationoperator.AnswerModelRequest) (applicationoperator.AnswerModelResponse, error) {
	if client == nil || client.httpClient == nil {
		return applicationoperator.AnswerModelResponse{}, errors.New("chat completions client is nil")
	}
	if strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.QuestionText) == "" {
		return applicationoperator.AnswerModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "instruction and question text are required",
		}
	}
	input, err := json.Marshal(request)
	if err != nil {
		return applicationoperator.AnswerModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "encode answer request", Cause: err,
		}
	}
	if len(input) > maximumRequestBytes {
		return applicationoperator.AnswerModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "answer request is too large",
		}
	}
	content, meta, err := client.createCompletion(ctx, "chat.completions", request.Instruction, "Answer this question:\n"+string(input),
		`{"selected_options": [string], "text": string, "confidence": "high"|"medium"|"low"}`)
	if err != nil {
		return applicationoperator.AnswerModelResponse{}, err
	}
	var result struct {
		SelectedOptions []string `json:"selected_options"`
		Text            string   `json:"text"`
		Confidence      string   `json:"confidence"`
	}
	if err := decodeStructured(content, &result); err != nil {
		return applicationoperator.AnswerModelResponse{}, err
	}
	return applicationoperator.AnswerModelResponse{
		SelectedOptions: result.SelectedOptions, Text: result.Text, Confidence: result.Confidence,
		Model: meta.model, ResponseID: meta.responseID,
	}, nil
}

// Select asks the model to choose which vacancy skills to
// append to the current resume. The caller still validates every decision
// against current skills, allowed paths and the platform limit.
func (client *Client) Select(ctx context.Context, request applicationoperator.ResumeTailoringModelRequest) (applicationoperator.ResumeTailoringModelResponse, error) {
	if client == nil || client.httpClient == nil {
		return applicationoperator.ResumeTailoringModelResponse{}, errors.New("chat completions client is nil")
	}
	if strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.PromptVersion) == "" {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "instruction and prompt version are required",
		}
	}
	contextJSON, err := json.Marshal(request)
	if err != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "encode resume tailoring context", Cause: err,
		}
	}
	if len(contextJSON) > maximumRequestBytes {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "resume tailoring context is too large",
		}
	}
	content, meta, err := client.createCompletion(ctx, "chat.completions", request.Instruction,
		"Select resume skills from this structured context JSON:\n"+string(contextJSON),
		`{"skills": [{"value": string, "action": "add"|"keep"|"remove", "evidence": string}]}`)
	if err != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, err
	}
	var result struct {
		Skills []applicationoperator.ResumeTailoringModelDecision `json:"skills"`
	}
	if err := decodeStructured(content, &result); err != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, err
	}
	return applicationoperator.ResumeTailoringModelResponse{
		Skills: result.Skills, Model: meta.model, ResponseID: meta.responseID,
	}, nil
}

// RewriteAbout asks the model to rewrite the resume "About" section. The
// caller anonymizes the context before the call and validates the result
// locally against the observed resume and the vacancy.
func (client *Client) RewriteAbout(ctx context.Context, request applicationoperator.ResumeTailoringAboutRequest) (applicationoperator.ResumeTailoringAboutResponse, error) {
	if client == nil || client.httpClient == nil {
		return applicationoperator.ResumeTailoringAboutResponse{}, errors.New("chat completions client is nil")
	}
	if strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.PromptVersion) == "" {
		return applicationoperator.ResumeTailoringAboutResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "instruction and prompt version are required",
		}
	}
	contextJSON, err := json.Marshal(request)
	if err != nil {
		return applicationoperator.ResumeTailoringAboutResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "encode resume tailoring about context", Cause: err,
		}
	}
	if len(contextJSON) > maximumRequestBytes {
		return applicationoperator.ResumeTailoringAboutResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "resume tailoring about context is too large",
		}
	}
	content, meta, err := client.createCompletion(ctx, "chat.completions", request.Instruction,
		"Rewrite the resume about text from this structured context JSON:\n"+string(contextJSON),
		`{"about": string}`)
	if err != nil {
		return applicationoperator.ResumeTailoringAboutResponse{}, err
	}
	var result struct {
		About string `json:"about"`
	}
	if err := decodeStructured(content, &result); err != nil {
		return applicationoperator.ResumeTailoringAboutResponse{}, err
	}
	return applicationoperator.ResumeTailoringAboutResponse{
		About: result.About, Model: meta.model, ResponseID: meta.responseID,
	}, nil
}

// RewriteExperience asks the model to rewrite work experience descriptions.
// The operator validates and grounds the result locally before applying it.
func (client *Client) RewriteExperience(ctx context.Context, request applicationoperator.ResumeTailoringExperienceRequest) (applicationoperator.ResumeTailoringExperienceResponse, error) {
	if client == nil || client.httpClient == nil {
		return applicationoperator.ResumeTailoringExperienceResponse{}, errors.New("chat completions client is nil")
	}
	if strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.PromptVersion) == "" {
		return applicationoperator.ResumeTailoringExperienceResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "instruction and prompt version are required",
		}
	}
	contextJSON, err := json.Marshal(request)
	if err != nil {
		return applicationoperator.ResumeTailoringExperienceResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "encode resume tailoring experience context", Cause: err,
		}
	}
	if len(contextJSON) > maximumRequestBytes {
		return applicationoperator.ResumeTailoringExperienceResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "chat.completions", Message: "resume tailoring experience context is too large",
		}
	}
	content, meta, err := client.createCompletion(ctx, "chat.completions", request.Instruction,
		"Rewrite the resume experience descriptions from this structured context JSON:\n"+string(contextJSON),
		`{"entries": [{"id": string, "description": string}], "order": [id]}`)
	if err != nil {
		return applicationoperator.ResumeTailoringExperienceResponse{}, err
	}
	var result struct {
		Entries []applicationoperator.ResumeTailoringExperienceEntry `json:"entries"`
		Order   []string                                             `json:"order,omitempty"`
	}
	if err := decodeStructured(content, &result); err != nil {
		return applicationoperator.ResumeTailoringExperienceResponse{}, err
	}
	return applicationoperator.ResumeTailoringExperienceResponse{
		Entries: result.Entries, Order: result.Order, Model: meta.model, ResponseID: meta.responseID,
	}, nil
}

type completionMeta struct {
	model      string
	responseID string
}

func (client *Client) createCompletion(ctx context.Context, operation, instructions, input, jsonShape string) (string, completionMeta, error) {
	if err := ctx.Err(); err != nil {
		return "", completionMeta{}, err
	}
	payload, err := json.Marshal(struct {
		Model           string            `json:"model"`
		Messages        []completionRole  `json:"messages"`
		MaxTokens       int               `json:"max_tokens"`
		ResponseFormat  map[string]string `json:"response_format"`
		ReasoningEffort string            `json:"reasoning_effort,omitempty"`
		Stream          bool              `json:"stream"`
	}{
		Model: client.model,
		Messages: []completionRole{
			{Role: "system", Content: instructions + "\n\nReturn a single JSON object and nothing else. Required shape: " + jsonShape},
			{Role: "user", Content: input},
		},
		MaxTokens: client.maxOutputTokens, ResponseFormat: map[string]string{"type": "json_object"},
		ReasoningEffort: client.reasoningEffort, Stream: false,
	})
	if err != nil {
		return "", completionMeta{}, fmt.Errorf("encode chat completions request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", completionMeta{}, fmt.Errorf("create chat completions request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")

	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return "", completionMeta{}, ctx.Err()
		}
		return "", completionMeta{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: operation, Message: "request failed", Cause: err,
		}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return "", completionMeta{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: operation, StatusCode: response.StatusCode, Message: "read response", Cause: err,
		}
	}
	if len(body) > maximumResponseBytes {
		return "", completionMeta{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: operation, StatusCode: response.StatusCode, Message: "response is too large",
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", completionMeta{}, responseStatusError(operation, response.StatusCode, body)
	}
	var decoded completionEnvelope
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", completionMeta{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: operation, StatusCode: response.StatusCode, Message: "decode response", Cause: err,
		}
	}
	if decoded.Error != nil {
		return "", completionMeta{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: operation, StatusCode: response.StatusCode, Message: truncateMessage(decoded.Error.Message),
		}
	}
	if len(decoded.Choices) == 0 {
		return "", completionMeta{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: operation, StatusCode: response.StatusCode, Message: "response contains no choices",
		}
	}
	choice := decoded.Choices[0]
	if choice.FinishReason == "length" {
		return "", completionMeta{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: operation, StatusCode: response.StatusCode, Message: "model output was truncated",
		}
	}
	content := strings.TrimSpace(choice.Message.Content)
	if content == "" {
		return "", completionMeta{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: operation, StatusCode: response.StatusCode, Message: "model returned empty content",
		}
	}
	return content, completionMeta{model: decoded.Model, responseID: decoded.ID}, nil
}

type completionRole struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionEnvelope struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// decodeStructured parses the model JSON object, tolerating a fenced code
// block because some OpenAI-compatible endpoints ignore response_format.
func decodeStructured(content string, target any) error {
	cleaned := stripJSONFence(content)
	decoder := json.NewDecoder(strings.NewReader(cleaned))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "chat.completions",
			Message: "decode structured output", Cause: err,
		}
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "chat.completions",
			Message: "decode structured output", Cause: err,
		}
	}
	return nil
}

func stripJSONFence(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	if index := strings.IndexByte(trimmed, '\n'); index >= 0 {
		trimmed = trimmed[index+1:]
	}
	trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
	return strings.TrimSpace(trimmed)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("structured output contains trailing JSON")
	}
	return err
}

func responseStatusError(operation string, statusCode int, body []byte) error {
	kind := applicationoperator.ModelFailurePermanent
	switch {
	case statusCode == http.StatusRequestTimeout:
		kind = applicationoperator.ModelFailureTimeout
	case statusCode == http.StatusTooManyRequests:
		kind = applicationoperator.ModelFailureRateLimited
	case statusCode >= http.StatusInternalServerError:
		kind = applicationoperator.ModelFailureTemporary
	}
	message := http.StatusText(statusCode)
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && strings.TrimSpace(payload.Error.Message) != "" {
		message = truncateMessage(payload.Error.Message)
	}
	return &applicationoperator.ModelError{
		Kind: kind, Operation: operation, StatusCode: statusCode, Message: message,
	}
}

func truncateMessage(value string) string {
	value = strings.TrimSpace(value)
	const maximum = 512
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
