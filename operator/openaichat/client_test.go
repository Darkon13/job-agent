package openaichat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	applicationoperator "github.com/Darkon13/job-agent/operator"
)

type completionRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	MaxTokens      int               `json:"max_tokens"`
	ResponseFormat map[string]string `json:"response_format"`
	Stream         bool              `json:"stream"`
}

func completionServer(t *testing.T, reply func(request completionRequest) string) (*httptest.Server, *completionRequest) {
	t.Helper()
	received := &completionRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(request.Body).Decode(received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(reply(*received)))
	}))
	return server, received
}

func chatClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "secret", Model: "deepseek-chat", MaxOutputTokens: 700})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func TestClientRewritesResumeTailoringAbout(t *testing.T) {
	server, received := completionServer(t, func(completionRequest) string {
		return `{"id":"chat-1","model":"deepseek-chat","choices":[{"finish_reason":"stop","message":{"content":"{\"about\":\"Пишу на Go и PostgreSQL.\"}"}}]}`
	})
	defer server.Close()
	result, err := chatClient(t, server).RewriteAbout(context.Background(), applicationoperator.ResumeTailoringAboutRequest{
		Instruction: "Highlight Go", PromptVersion: "v1", VacancyTitle: "Go developer",
		CurrentAbout: "Backend developer", MaximumRunes: 600,
	})
	if err != nil {
		t.Fatalf("rewrite about: %v", err)
	}
	if result.About != "Пишу на Go и PostgreSQL." || result.Model != "deepseek-chat" || result.ResponseID != "chat-1" {
		t.Fatalf("result = %#v", result)
	}
	if received.Model != "deepseek-chat" || received.Stream || received.ResponseFormat["type"] != "json_object" ||
		len(received.Messages) != 2 || received.Messages[0].Role != "system" || !strings.Contains(received.Messages[0].Content, "JSON") ||
		!strings.Contains(received.Messages[1].Content, `"vacancy_title":"Go developer"`) {
		t.Fatalf("request = %#v", received)
	}
}

func TestClientSelectsResumeTailoringSkills(t *testing.T) {
	server, _ := completionServer(t, func(completionRequest) string {
		content := "```json\n{\"skills\":[{\"value\":\"PostgreSQL\",\"action\":\"add\",\"evidence\":\"vacancy.key_skills\"}]}\n```"
		return `{"id":"chat-2","model":"deepseek-chat","choices":[{"finish_reason":"stop","message":{"content":` + strconv.Quote(content) + `}}]}`
	})
	defer server.Close()
	result, err := chatClient(t, server).Select(context.Background(), applicationoperator.ResumeTailoringModelRequest{
		Instruction: "Prefer Go", PromptVersion: "v1", VacancyTitle: "Go developer",
		VacancySkills: []string{"Go", "PostgreSQL"}, CurrentSkills: []string{"Go"}, MaximumSkills: 2,
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(result.Skills) != 1 || result.Skills[0].Value != "PostgreSQL" || result.Skills[0].Action != "add" {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientGeneratesCoverLetterEvidence(t *testing.T) {
	server, _ := completionServer(t, func(completionRequest) string {
		return `{"id":"chat-3","model":"deepseek-chat","choices":[{"finish_reason":"stop","message":{"content":"{\"text\":\"Письмо\",\"evidence\":[{\"claim\":\"Письмо\",\"sources\":[{\"path\":\"/resume/facts/position\",\"quote\":\"Go developer\"}]}]}"}}]}`
	})
	defer server.Close()
	result, err := chatClient(t, server).Generate(context.Background(), applicationoperator.ApplicationModelRequest{
		Instruction: "Write", PromptVersion: "v1",
		Context: applicationoperator.ApplicationTemplateData{Vacancy: applicationoperator.ApplicationVacancyContext{Title: "Go developer"}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if result.Text != "Письмо" || len(result.Evidence) != 1 || result.Evidence[0].Sources[0].Path != "/resume/facts/position" {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientRejectsMalformedAndTruncatedOutput(t *testing.T) {
	server, _ := completionServer(t, func(completionRequest) string {
		return `{"id":"chat-4","model":"deepseek-chat","choices":[{"finish_reason":"stop","message":{"content":"not json"}}]}`
	})
	defer server.Close()
	if _, err := chatClient(t, server).RewriteAbout(context.Background(), applicationoperator.ResumeTailoringAboutRequest{
		Instruction: "Rewrite", PromptVersion: "v1", MaximumRunes: 600,
	}); applicationoperator.ModelFailureKindOf(err) != applicationoperator.ModelFailureInvalidOutput {
		t.Fatalf("malformed error = %v", err)
	}

	truncated, _ := completionServer(t, func(completionRequest) string {
		return `{"id":"chat-5","model":"deepseek-chat","choices":[{"finish_reason":"length","message":{"content":"{\"about\":\"half"}}]}`
	})
	defer truncated.Close()
	if _, err := chatClient(t, truncated).RewriteAbout(context.Background(), applicationoperator.ResumeTailoringAboutRequest{
		Instruction: "Rewrite", PromptVersion: "v1", MaximumRunes: 600,
	}); applicationoperator.ModelFailureKindOf(err) != applicationoperator.ModelFailureInvalidOutput {
		t.Fatalf("truncated error = %v", err)
	}
}

func TestClientClassifiesProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{"error":{"message":"rate limit"}}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "secret", Model: "deepseek-chat"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.RewriteAbout(context.Background(), applicationoperator.ResumeTailoringAboutRequest{
		Instruction: "Rewrite", PromptVersion: "v1", MaximumRunes: 600,
	}); applicationoperator.ModelFailureKindOf(err) != applicationoperator.ModelFailureRateLimited {
		t.Fatalf("rate limit error = %v", err)
	}
}

func TestClientRequiresHTTPSOrLoopback(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://models.example.test/v1", APIKey: "secret", Model: "deepseek-chat"}); err == nil {
		t.Fatal("expected non-loopback HTTP base URL to fail")
	}
	if _, err := New(Config{BaseURL: "http://127.0.0.1:11434/v1", APIKey: "secret", Model: "deepseek-chat"}); err != nil {
		t.Fatalf("loopback HTTP base URL: %v", err)
	}
}
