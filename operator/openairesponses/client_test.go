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

func TestClientCreatesPrivateResponseAndCollectsOutputText(t *testing.T) {
	var received struct {
		Model           string              `json:"model"`
		Instructions    string              `json:"instructions"`
		Input           string              `json:"input"`
		Text            responsesTextConfig `json:"text"`
		MaxOutputTokens int                 `json:"max_output_tokens"`
		Store           bool                `json:"store"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request = %s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"id":"response-1","model":"gpt-test","status":"completed",
			"output":[
				{"type":"reasoning","content":[]},
				{"type":"message","content":[
					{"type":"output_text","text":"{\"text\":\"Backend\",\"evidence\":[{\"claim\":\"Backend\",\"sources\":[{\"path\":\"/vacancy/title\",\"quote\":\"Backend\"}]}]}"}
				]}
			]
		}`))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "secret", Model: "gpt-test", MaxOutputTokens: 700})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	result, err := client.Generate(context.Background(), applicationoperator.ApplicationModelRequest{
		Instruction: "Use only facts", PromptVersion: "v1",
		Context: applicationoperator.ApplicationTemplateData{
			ApplicationID: "application-1", ProfileID: "primary",
			Vacancy: applicationoperator.ApplicationVacancyContext{Platform: "hh", ExternalID: "42", Title: "Backend"},
		},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if result.Text != "Backend" || len(result.Evidence) != 1 || result.Evidence[0].Sources[0].Path != "/vacancy/title" ||
		result.Model != "gpt-test" || result.ResponseID != "response-1" {
		t.Fatalf("result = %#v", result)
	}
	if received.Store || received.Model != "gpt-test" || received.MaxOutputTokens != 700 || received.Instructions != "Use only facts" ||
		!strings.Contains(received.Input, `"external_id":"42"`) || received.Text.Format.Type != "json_schema" ||
		received.Text.Format.Name != "application_cover_letter" || !received.Text.Format.Strict ||
		received.Text.Format.Schema["additionalProperties"] != false {
		t.Fatalf("request body = %#v", received)
	}
}

func TestClientClassifiesHTTPFailures(t *testing.T) {
	tests := []struct {
		status int
		kind   applicationoperator.ModelFailureKind
	}{
		{status: http.StatusRequestTimeout, kind: applicationoperator.ModelFailureTimeout},
		{status: http.StatusTooManyRequests, kind: applicationoperator.ModelFailureRateLimited},
		{status: http.StatusBadRequest, kind: applicationoperator.ModelFailurePermanent},
		{status: http.StatusServiceUnavailable, kind: applicationoperator.ModelFailureTemporary},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(`{"error":{"message":"provider rejected request"}}`))
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, APIKey: "secret", Model: "gpt-test"})
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			_, err = client.Generate(context.Background(), applicationoperator.ApplicationModelRequest{
				Instruction: "instruction", PromptVersion: "v1",
			})
			var modelError *applicationoperator.ModelError
			if !errors.As(err, &modelError) || modelError.Kind != test.kind || modelError.StatusCode != test.status {
				t.Fatalf("error = %#v, want kind %q", err, test.kind)
			}
		})
	}
}

func TestClientRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	for _, config := range []Config{
		{BaseURL: "http://api.example.test/v1", APIKey: "secret", Model: "model"},
		{BaseURL: "https://user:password@example.test/v1", APIKey: "secret", Model: "model"},
		{BaseURL: "https://api.example.test/v1", Model: "model"},
		{BaseURL: "https://api.example.test/v1", APIKey: "secret"},
		{BaseURL: "https://api.example.test/v1", APIKey: "secret", Model: "model", MaxOutputTokens: maximumMaxOutputTokens + 1},
	} {
		if _, err := New(config); err == nil {
			t.Fatalf("expected config to fail: %#v", config)
		}
	}
}

func TestClientRejectsNonCompletedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"id":"response-1","model":"gpt-test","status":"incomplete","output":[]}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "secret", Model: "gpt-test"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Generate(context.Background(), applicationoperator.ApplicationModelRequest{Instruction: "instruction", PromptVersion: "v1"})
	if applicationoperator.ModelFailureKindOf(err) != applicationoperator.ModelFailureInvalidOutput {
		t.Fatalf("error = %v", err)
	}
}

func TestClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(strings.Repeat("x", maximumResponseBytes+1)))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "secret", Model: "gpt-test"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Generate(context.Background(), applicationoperator.ApplicationModelRequest{Instruction: "instruction", PromptVersion: "v1"})
	if applicationoperator.ModelFailureKindOf(err) != applicationoperator.ModelFailureInvalidOutput {
		t.Fatalf("error = %v", err)
	}
}

func TestClientRejectsMalformedStructuredOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id":"response-1","model":"gpt-test","status":"completed",
			"output_text":"{\"text\":\"Backend\",\"evidence\":[],\"unexpected\":true}"
		}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "secret", Model: "gpt-test"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Generate(context.Background(), applicationoperator.ApplicationModelRequest{Instruction: "instruction", PromptVersion: "v1"})
	if applicationoperator.ModelFailureKindOf(err) != applicationoperator.ModelFailureInvalidOutput {
		t.Fatalf("error = %v", err)
	}
}
