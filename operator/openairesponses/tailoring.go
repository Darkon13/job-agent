package openairesponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	applicationoperator "github.com/Darkon13/job-agent/operator"
)

var _ applicationoperator.ResumeTailoringModel = (*Client)(nil)
var _ applicationoperator.ResumeTailoringAboutModel = (*Client)(nil)

// Select asks the model to choose which vacancy skills to
// append to the current resume. The caller still validates every decision
// against current skills, allowed paths and the platform limit.
func (client *Client) Select(ctx context.Context, request applicationoperator.ResumeTailoringModelRequest) (applicationoperator.ResumeTailoringModelResponse, error) {
	if client == nil || client.httpClient == nil {
		return applicationoperator.ResumeTailoringModelResponse{}, errors.New("OpenAI Responses client is nil")
	}
	if err := ctx.Err(); err != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, err
	}
	if strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.PromptVersion) == "" {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "instruction and prompt version are required",
		}
	}
	contextJSON, err := json.Marshal(request)
	if err != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "encode resume tailoring context", Cause: err,
		}
	}
	if len(contextJSON) > maximumRequestBytes {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "resume tailoring context is too large",
		}
	}
	payload, err := json.Marshal(struct {
		Model           string              `json:"model"`
		Instructions    string              `json:"instructions"`
		Input           string              `json:"input"`
		Text            responsesTextConfig `json:"text"`
		MaxOutputTokens int                 `json:"max_output_tokens"`
		Store           bool                `json:"store"`
	}{
		Model: client.model, Instructions: request.Instruction,
		Input:           "Select resume skills from this structured context JSON:\n" + string(contextJSON),
		Text:            resumeTailoringResponseTextConfig(),
		MaxOutputTokens: client.maxOutputTokens, Store: false,
	})
	if err != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, fmt.Errorf("encode OpenAI Responses request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(payload))
	if err != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, fmt.Errorf("create OpenAI Responses request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")

	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return applicationoperator.ResumeTailoringModelResponse{}, ctx.Err()
		}
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: "responses.create", Message: "request failed", Cause: err,
		}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: "responses.create", StatusCode: response.StatusCode, Message: "read response", Cause: err,
		}
	}
	if len(body) > maximumResponseBytes {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create", StatusCode: response.StatusCode, Message: "response is too large",
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return applicationoperator.ResumeTailoringModelResponse{}, responseStatusError("responses.create", response.StatusCode, body)
	}
	var decoded responseEnvelope
	if err := json.Unmarshal(body, &decoded); err != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create", StatusCode: response.StatusCode, Message: "decode response", Cause: err,
		}
	}
	if decoded.Error != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureTemporary, Operation: "responses.create", StatusCode: response.StatusCode, Message: truncateMessage(decoded.Error.Message),
		}
	}
	if decoded.Status != "completed" {
		kind := applicationoperator.ModelFailureInvalidOutput
		if decoded.Status == "failed" {
			kind = applicationoperator.ModelFailureTemporary
		}
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: kind, Operation: "responses.create", StatusCode: response.StatusCode, Message: "unexpected response status " + decoded.Status,
		}
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
		Skills []applicationoperator.ResumeTailoringModelDecision `json:"skills"`
	}
	outputDecoder := json.NewDecoder(strings.NewReader(structuredOutput))
	outputDecoder.DisallowUnknownFields()
	if err := outputDecoder.Decode(&result); err != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create", StatusCode: response.StatusCode,
			Message: "decode structured output", Cause: err,
		}
	}
	if err := ensureJSONEOF(outputDecoder); err != nil {
		return applicationoperator.ResumeTailoringModelResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create", StatusCode: response.StatusCode,
			Message: "decode structured output", Cause: err,
		}
	}
	return applicationoperator.ResumeTailoringModelResponse{
		Skills: result.Skills, Model: decoded.Model, ResponseID: decoded.ID,
	}, nil
}

// RewriteExperience asks the model to rewrite work experience descriptions.
// The caller validates the result locally against each entry and the vacancy.
func (client *Client) RewriteExperience(ctx context.Context, request applicationoperator.ResumeTailoringExperienceRequest) (applicationoperator.ResumeTailoringExperienceResponse, error) {
	if client == nil || client.httpClient == nil {
		return applicationoperator.ResumeTailoringExperienceResponse{}, errors.New("OpenAI Responses client is nil")
	}
	if err := ctx.Err(); err != nil {
		return applicationoperator.ResumeTailoringExperienceResponse{}, err
	}
	if strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.PromptVersion) == "" {
		return applicationoperator.ResumeTailoringExperienceResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "instruction and prompt version are required",
		}
	}
	contextJSON, err := json.Marshal(request)
	if err != nil {
		return applicationoperator.ResumeTailoringExperienceResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "encode resume tailoring experience context", Cause: err,
		}
	}
	if len(contextJSON) > maximumRequestBytes {
		return applicationoperator.ResumeTailoringExperienceResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "resume tailoring experience context is too large",
		}
	}
	decoded, err := client.createResponse(ctx, "responses.create", request.Instruction,
		"Rewrite the resume experience descriptions from this structured context JSON:\n"+string(contextJSON), resumeTailoringExperienceTextConfig())
	if err != nil {
		return applicationoperator.ResumeTailoringExperienceResponse{}, err
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
		Entries []applicationoperator.ResumeTailoringExperienceEntry `json:"entries"`
	}
	outputDecoder := json.NewDecoder(strings.NewReader(structuredOutput))
	outputDecoder.DisallowUnknownFields()
	if err := outputDecoder.Decode(&result); err != nil {
		return applicationoperator.ResumeTailoringExperienceResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create",
			Message: "decode structured output", Cause: err,
		}
	}
	if err := ensureJSONEOF(outputDecoder); err != nil {
		return applicationoperator.ResumeTailoringExperienceResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create",
			Message: "decode structured output", Cause: err,
		}
	}
	return applicationoperator.ResumeTailoringExperienceResponse{
		Entries: result.Entries, Model: decoded.Model, ResponseID: decoded.ID,
	}, nil
}

// RewriteAbout asks the model to rewrite the resume "About" section. The
// caller anonymizes the context before the call and validates the result
// locally against resume facts and the vacancy.
func (client *Client) RewriteAbout(ctx context.Context, request applicationoperator.ResumeTailoringAboutRequest) (applicationoperator.ResumeTailoringAboutResponse, error) {
	if client == nil || client.httpClient == nil {
		return applicationoperator.ResumeTailoringAboutResponse{}, errors.New("OpenAI Responses client is nil")
	}
	if err := ctx.Err(); err != nil {
		return applicationoperator.ResumeTailoringAboutResponse{}, err
	}
	if strings.TrimSpace(request.Instruction) == "" || strings.TrimSpace(request.PromptVersion) == "" {
		return applicationoperator.ResumeTailoringAboutResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "instruction and prompt version are required",
		}
	}
	contextJSON, err := json.Marshal(request)
	if err != nil {
		return applicationoperator.ResumeTailoringAboutResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "encode resume tailoring about context", Cause: err,
		}
	}
	if len(contextJSON) > maximumRequestBytes {
		return applicationoperator.ResumeTailoringAboutResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailurePermanent, Operation: "responses.create", Message: "resume tailoring about context is too large",
		}
	}
	decoded, err := client.createResponse(ctx, "responses.create", request.Instruction,
		"Rewrite the resume about text from this structured context JSON:\n"+string(contextJSON), resumeTailoringAboutTextConfig())
	if err != nil {
		return applicationoperator.ResumeTailoringAboutResponse{}, err
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
		About string `json:"about"`
	}
	outputDecoder := json.NewDecoder(strings.NewReader(structuredOutput))
	outputDecoder.DisallowUnknownFields()
	if err := outputDecoder.Decode(&result); err != nil {
		return applicationoperator.ResumeTailoringAboutResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create",
			Message: "decode structured output", Cause: err,
		}
	}
	if err := ensureJSONEOF(outputDecoder); err != nil {
		return applicationoperator.ResumeTailoringAboutResponse{}, &applicationoperator.ModelError{
			Kind: applicationoperator.ModelFailureInvalidOutput, Operation: "responses.create",
			Message: "decode structured output", Cause: err,
		}
	}
	return applicationoperator.ResumeTailoringAboutResponse{
		About: result.About, Model: decoded.Model, ResponseID: decoded.ID,
	}, nil
}

func resumeTailoringExperienceTextConfig() responsesTextConfig {
	return responsesTextConfig{Format: responsesTextFormat{
		Type: "json_schema", Name: "resume_tailoring_experience", Strict: true,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"entries": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":          map[string]any{"type": "string", "description": "Entry id from the request"},
							"description": map[string]any{"type": "string", "description": "Rewritten experience description"},
						},
						"required":             []string{"id", "description"},
						"additionalProperties": false,
					},
				},
				"order": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
			},
			"required":             []string{"entries"},
			"additionalProperties": false,
		},
	}}
}

func resumeTailoringAboutTextConfig() responsesTextConfig {
	return responsesTextConfig{Format: responsesTextFormat{
		Type: "json_schema", Name: "resume_tailoring_about", Strict: true,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"about": map[string]any{"type": "string", "description": "Rewritten 'About' section text"},
			},
			"required":             []string{"about"},
			"additionalProperties": false,
		},
	}}
}

func resumeTailoringResponseTextConfig() responsesTextConfig {
	decisionSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value":    map[string]any{"type": "string", "description": "Skill text from vacancy_skills or current_skills"},
			"action":   map[string]any{"type": "string", "enum": []string{"add", "keep"}},
			"evidence": map[string]any{"type": "string", "description": "Non-empty source such as vacancy.key_skills"},
		},
		"required":             []string{"value", "action", "evidence"},
		"additionalProperties": false,
	}
	return responsesTextConfig{Format: responsesTextFormat{
		Type: "json_schema", Name: "resume_tailoring_skills", Strict: true,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skills": map[string]any{
					"type": "array", "items": decisionSchema,
				},
			},
			"required":             []string{"skills"},
			"additionalProperties": false,
		},
	}}
}
