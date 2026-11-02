package hhapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// GenerateText — текстовый вызов модели через OpenAI-совместимый API.
func GenerateText(ctx context.Context, apiKey, baseURL, model, prompt string) (string, error) {
	payload := map[string]any{
		"model": model, "reasoning_effort": "none", "temperature": 0.3, "max_tokens": 2000,
		"messages": []any{map[string]any{"role": "user", "content": prompt}},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-opencode-session", "job-agent-local")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("model: %s", string(raw[:min(len(raw), 200)]))
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("model: empty response")
	}
	text := strings.TrimSpace(result.Choices[0].Message.Content)
	if text == "" {
		text = strings.TrimSpace(result.Choices[0].Message.ReasoningContent)
	}
	if text == "" {
		return "", fmt.Errorf("model: empty text")
	}
	return text, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
