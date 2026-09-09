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
	if err := ctx.Err(); err != nil {
		return applicationoperator.ApplicationModelResponse{}, err
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
	payload, err := json.Marshal(struct {
		Model           string `json:"model"`
		Instructions    string `json:"instructions"`
		Input           string `json:"input"`
		MaxOutputTokens int    `json:"max_output_tokens"`
		Store           bool   `json:"store"`
	}{
		Model: client.model, Instructions: request.Instruction,
		Input:           "Generate a cover letter from this structured context JSON:\n" + string(contextJSON),
		MaxOutputTokens: client.maxOutputTokens, Store: false,
	})
	if err != nil {
		return applicationoperator.ApplicationModelResponse{}, fmt.Errorf("encode OpenAI Responses request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(payload))
	if err != nil {
		return applicationoperator.ApplicationModelResponse{}, fmt.Errorf("create OpenAI Responses request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")

	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return applicationoperator.ApplicationModelResponse{}, ctx.Err()
		}
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: "responses.create", Message: "request failed", Cause: err,
		}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: "responses.create", StatusCode: response.StatusCode, Message: "read response", Cause: err,
		}
	}
	if len(body) > maximumResponseBytes {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create", StatusCode: response.StatusCode, Message: "response is too large",
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return applicationoperator.ApplicationModelResponse{}, responseStatusError(response.StatusCode, body)
	}

	var decoded responseEnvelope
	if err := json.Unmarshal(body, &decoded); err != nil {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create", StatusCode: response.StatusCode, Message: "decode response", Cause: err,
		}
	}
	if decoded.Error != nil {
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: "responses.create", StatusCode: response.StatusCode, Message: truncateMessage(decoded.Error.Message),
		}
	}
	if decoded.Status != "completed" {
		kind := applicationoperator.ModelFailureInvalidOutput
		if decoded.Status == "failed" {
			kind = applicationoperator.ModelFailureTemporary
		}
		return applicationoperator.ApplicationModelResponse{}, &applicationoperator.ModelError{
			Kind: kind, Operation: "responses.create", StatusCode: response.StatusCode, Message: "unexpected response status " + decoded.Status,
		}
	}
	text := strings.TrimSpace(decoded.OutputText)
	if text == "" {
		parts := make([]string, 0)
		for _, output := range decoded.Output {
			for _, content := range output.Content {
				if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
					parts = append(parts, strings.TrimSpace(content.Text))
				}
			}
		}
		text = strings.Join(parts, "\n")
	}
	return applicationoperator.ApplicationModelResponse{
		Text: text, Model: decoded.Model, ResponseID: decoded.ID,
	}, nil
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

func responseStatusError(statusCode int, body []byte) error {
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
		Kind: kind, Operation: "responses.create", StatusCode: statusCode, Message: message,
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
