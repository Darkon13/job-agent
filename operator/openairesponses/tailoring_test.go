package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	applicationoperator "github.com/Darkon13/job-agent/operator"
)

func TestClientSelectsResumeTailoringSkills(t *testing.T) {
	var received struct {
		Model           string              `json:"model"`
		Instructions    string              `json:"instructions"`
		Input           string              `json:"input"`
		Text            responsesTextConfig `json:"text"`
		MaxOutputTokens int                 `json:"max_output_tokens"`
		Store           bool                `json:"store"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"id":"response-2","model":"gpt-test","status":"completed",
			"output_text":"{\"skills\":[{\"value\":\"PostgreSQL\",\"action\":\"add\",\"evidence\":\"vacancy.key_skills\"}]}"
		}`))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "secret", Model: "gpt-test", MaxOutputTokens: 700})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	result, err := client.Select(context.Background(), applicationoperator.ResumeTailoringModelRequest{
		Instruction: "Prefer Go", PromptVersion: "v1", VacancyTitle: "Go developer",
		VacancySkills: []string{"Go", "PostgreSQL"}, CurrentSkills: []string{"Go"}, MaximumSkills: 2,
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(result.Skills) != 1 || result.Skills[0].Value != "PostgreSQL" || result.Skills[0].Action != "add" ||
		result.Model != "gpt-test" || result.ResponseID != "response-2" {
		t.Fatalf("result = %#v", result)
	}
	if received.Store || received.Text.Format.Name != "resume_tailoring_skills" || !received.Text.Format.Strict ||
		!strings.Contains(received.Input, `"maximum_skills":2`) || !strings.Contains(received.Input, `"vacancy_skills":["Go","PostgreSQL"]`) {
		t.Fatalf("request body = %#v", received)
	}
}

func TestClientRewritesResumeTailoringAbout(t *testing.T) {
	var received struct {
		Model        string              `json:"model"`
		Instructions string              `json:"instructions"`
		Input        string              `json:"input"`
		Text         responsesTextConfig `json:"text"`
		Store        bool                `json:"store"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"id":"response-4","model":"gpt-test","status":"completed",
			"output_text":"{\"about\":\"Опытный {name} пишет на Go.\"}"
		}`))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "secret", Model: "gpt-test", MaxOutputTokens: 700})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	result, err := client.RewriteAbout(context.Background(), applicationoperator.ResumeTailoringAboutRequest{
		Instruction: "Highlight Go", PromptVersion: "v1", VacancyTitle: "Go developer",
		CurrentAbout: "{name} пишет на Go", Facts: map[string]any{"city": "Москва"}, MaximumRunes: 600,
	})
	if err != nil {
		t.Fatalf("rewrite about: %v", err)
	}
	if result.About != "Опытный {name} пишет на Go." || result.Model != "gpt-test" || result.ResponseID != "response-4" {
		t.Fatalf("result = %#v", result)
	}
	if received.Store || received.Text.Format.Name != "resume_tailoring_about" || !received.Text.Format.Strict ||
		!strings.Contains(received.Input, `"maximum_runes":600`) || !strings.Contains(received.Input, `"city":"Москва"`) {
		t.Fatalf("request body = %#v", received)
	}
}

func TestClientRejectsMalformedTailoringOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"response-3","model":"gpt-test","status":"completed","output_text":"not json"}`))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, APIKey: "secret", Model: "gpt-test"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Select(context.Background(), applicationoperator.ResumeTailoringModelRequest{
		Instruction: "Prefer Go", PromptVersion: "v1",
	})
	var modelError *applicationoperator.ModelError
	if err == nil {
		t.Fatal("expected malformed output to fail")
	}
	if !errors.As(err, &modelError) || modelError.Kind != applicationoperator.ModelFailureInvalidOutput {
		t.Fatalf("error = %#v", err)
	}
}
