package openairesponses

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

const (
	DefaultBaseURL         = "https://api.openai.com/v1"
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
	HTTPClient      HTTPClient
}

var _ applicationoperator.ApplicationMessageModel = (*Client)(nil)
var _ applicationoperator.AnswerGenerator = (*Client)(nil)

type Client struct {
	endpoint        string
	apiKey          string
	model           string
	maxOutputTokens int
	httpClient      HTTPClient
}

func New(config Config) (*Client, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("OpenAI Responses base URL must be an absolute URL without credentials, query or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopbackHost(parsed.Hostname())) {
		return nil, errors.New("OpenAI Responses base URL must use HTTPS or loopback HTTP")
	}
	apiKey := strings.TrimSpace(config.APIKey)
	model := strings.TrimSpace(config.Model)
	if apiKey == "" || model == "" {
		return nil, errors.New("OpenAI Responses client requires API key and model")
	}
	maxOutputTokens := config.MaxOutputTokens
	if maxOutputTokens == 0 {
		maxOutputTokens = defaultMaxOutputTokens
	}
	if maxOutputTokens < 1 || maxOutputTokens > maximumMaxOutputTokens {
		return nil, fmt.Errorf("OpenAI Responses max output tokens must be between 1 and %d", maximumMaxOutputTokens)
	}
	endpoint, err := url.JoinPath(baseURL, "responses")
	if err != nil {
		return nil, fmt.Errorf("build OpenAI Responses endpoint: %w", err)
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		endpoint: endpoint, apiKey: apiKey, model: model,
		maxOutputTokens: maxOutputTokens, httpClient: httpClient,
	}, nil
}

func (client *Client) Generate(ctx context.Context, request applicationoperator.ApplicationModelRequest) (applicationoperator.ApplicationModelResponse, error) {
	if client == nil || client.httpClient == nil {
		return applicationoperator.ApplicationModelResponse{}, errors.New("OpenAI Responses client is nil")
	}
	if strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.PromptVersion) == "" {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "instruction and prompt version are required",
		}
	}
	contextJSON, err := json.Marshal(request.Context)
	if err != nil {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "encode application context", Cause: err,
		}
	}
	if len(contextJSON) > maximumRequestBytes {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "application context is too large",
		}
	}
	decoded, err := client.createResponse(ctx, "responses.create", request.Instruction,
		"Generate a cover letter from this structured context JSON:\n"+string(contextJSON), applicationResponseTextConfig())
	if err != nil {
		return applicationoperator.ApplicationModelResponse{}, err
	}
	structuredOutput := strings.TrimSpace(decoded.OutputText)
	if structuredOutput == "" {
		parts := make([]string, 0)
		for _, output := range decoded.Output {
			for _, content := range output.Content {
				if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
					parts = append(parts, content.Text)
				}
			}
		}
		structuredOutput = strings.TrimSpace(strings.Join(parts, ""))
	}
	var result struct {
		Text     string                                         `json:"text"`
		Evidence []applicationoperator.ApplicationModelEvidence `json:"evidence"`
	}
	outputDecoder := json.NewDecoder(strings.NewReader(structuredOutput))
	outputDecoder.DisallowUnknownFields()
	if err := outputDecoder.Decode(&result); err != nil {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create",
			Message: "decode structured output", Cause: err,
		}
	}
	if err := ensureJSONEOF(outputDecoder); err != nil {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create",
			Message: "decode structured output", Cause: err,
		}
	}
	return applicationoperator.ApplicationModelResponse{
		Text: result.Text, Evidence: result.Evidence, Model: decoded.Model, ResponseID: decoded.ID,
	}, nil
}

// GenerateAnswer asks for one bounded question answer. The provider schema is
// separate from the cover-letter request so no application context can leak
// into a test or questionnaire prompt.
func (client *Client) GenerateAnswer(ctx context.Context, request applicationoperator.AnswerModelRequest) (applicationoperator.AnswerModelResponse, error) {
	if client == nil || client.httpClient == nil {
		return applicationoperator.AnswerModelResponse{}, errors.New("OpenAI Responses client is nil")
	}
	if strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.QuestionText) == "" {
		return applicationoperator.AnswerModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "answers.create", Message: "instruction and question text are required",
		}
	}
	input, err := json.Marshal(request)
	if err != nil {
		return applicationoperator.AnswerModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "answers.create", Message: "encode answer request", Cause: err,
		}
	}
	if len(input) > maximumRequestBytes {
		return applicationoperator.AnswerModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "answers.create", Message: "answer request is too large",
		}
	}
	decoded, err := client.createResponse(ctx, "answers.create", request.Instruction,
		"Answer this question:\n"+string(input), answerResponseTextConfig())
	if err != nil {
		return applicationoperator.AnswerModelResponse{}, err
	}
	structuredOutput := strings.TrimSpace(decoded.OutputText)
	if structuredOutput == "" {
		parts := make([]string, 0)
		for _, output := range decoded.Output {
			for _, content := range output.Content {
				if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
					parts = append(parts, content.Text)
				}
			}
		}
		structuredOutput = strings.TrimSpace(strings.Join(parts, ""))
	}
	var result struct {
		SelectedOptions []string `json:"selected_options"`
		Text            string   `json:"text"`
		Confidence      string   `json:"confidence"`
	}
	outputDecoder := json.NewDecoder(strings.NewReader(structuredOutput))
	outputDecoder.DisallowUnknownFields()
	if err := outputDecoder.Decode(&result); err != nil {
		return applicationoperator.AnswerModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "answers.create",
			Message: "decode structured output", Cause: err,
		}
	}
	if err := ensureJSONEOF(outputDecoder); err != nil {
		return applicationoperator.AnswerModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "answers.create",
			Message: "decode structured output", Cause: err,
		}
	}
	return applicationoperator.AnswerModelResponse{
		SelectedOptions: result.SelectedOptions, Text: result.Text, Confidence: result.Confidence,
		Model: decoded.Model, ResponseID: decoded.ID,
	}, nil
}

func (client *Client) createResponse(ctx context.Context, operation, instructions, input string, text responsesTextConfig) (responseEnvelope, error) {
	if err := ctx.Err(); err != nil {
		return responseEnvelope{}, err
	}
	payload, err := json.Marshal(struct {
		Model           string              `json:"model"`
		Instructions    string              `json:"instructions"`
		Input           string              `json:"input"`
		Text            responsesTextConfig `json:"text"`
		MaxOutputTokens int                 `json:"max_output_tokens"`
		Store           bool                `json:"store"`
	}{
		Model: client.model, Instructions: instructions, Input: input,
		Text: text, MaxOutputTokens: client.maxOutputTokens, Store: false,
	})
	if err != nil {
		return responseEnvelope{}, fmt.Errorf("encode OpenAI Responses request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(payload))
	if err != nil {
		return responseEnvelope{}, fmt.Errorf("create OpenAI Responses request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")

	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return responseEnvelope{}, ctx.Err()
		}
		return responseEnvelope{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: operation, Message: "request failed", Cause: err,
		}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return responseEnvelope{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: operation, StatusCode: response.StatusCode, Message: "read response", Cause: err,
		}
	}
	if len(body) > maximumResponseBytes {
		return responseEnvelope{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: operation, StatusCode: response.StatusCode, Message: "response is too large",
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return responseEnvelope{}, responseStatusError(operation, response.StatusCode, body)
	}

	var decoded responseEnvelope
	if err := json.Unmarshal(body, &decoded); err != nil {
		return responseEnvelope{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: operation, StatusCode: response.StatusCode, Message: "decode response", Cause: err,
		}
	}
	if decoded.Error != nil {
		return responseEnvelope{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: operation, StatusCode: response.StatusCode, Message: truncateMessage(decoded.Error.Message),
		}
	}
	if decoded.Status != "completed" {
		kind := applicationoperator.ModelFailureInvalidOutput
		if decoded.Status == "failed" {
			kind = applicationoperator.ModelFailureTemporary
		}
		return responseEnvelope{}, &applicationoperator.ModelError{
			Kind: kind, Operation: operation, StatusCode: response.StatusCode, Message: "unexpected response status " + decoded.Status,
		}
	}
	return decoded, nil
}

type responsesTextConfig struct {
	Format responsesTextFormat `json:"format"`
}

type responsesTextFormat struct {
	Type   string         `json:"type"`
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

func applicationResponseTextConfig() responsesTextConfig {
	sourceSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string", "description": "RFC 6901 JSON Pointer to one scalar context value"},
			"quote": map[string]any{"type": "string", "description": "Exact quote occurring in both the source value and claim"},
		},
		"required":             []string{"path", "quote"},
		"additionalProperties": false,
	}
	evidenceSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"claim": map[string]any{"type": "string", "description": "Exact unique span of the generated cover letter"},
			"sources": map[string]any{
				"type": "array", "items": sourceSchema,
			},
		},
		"required":             []string{"claim", "sources"},
		"additionalProperties": false,
	}
	return responsesTextConfig{Format: responsesTextFormat{
		Type: "json_schema", Name: "application_cover_letter", Strict: true,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string", "description": "Cover letter text only"},
				"evidence": map[string]any{
					"type": "array", "items": evidenceSchema,
				},
			},
			"required":             []string{"text", "evidence"},
			"additionalProperties": false,
		},
	}}
}

func answerResponseTextConfig() responsesTextConfig {
	return responsesTextConfig{Format: responsesTextFormat{
		Type: "json_schema", Name: "qualification_answer", Strict: true,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"selected_options": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Exact presented option texts; empty for text questions",
				},
				"text": map[string]any{"type": "string", "description": "Open answer; empty for choice questions"},
				"confidence": map[string]any{
					"type": "string", "enum": []string{"high", "medium", "low"},
				},
			},
			"required":             []string{"selected_options", "text", "confidence"},
			"additionalProperties": false,
		},
	}}
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

type responseEnvelope struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	Status     string `json:"status"`
	OutputText string `json:"output_text"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
	Output []struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
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
