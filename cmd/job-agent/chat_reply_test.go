package main

import (
	"strings"
	"testing"
	"time"

	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
)

func TestChatReplyProviderUsesProfileAnswerProvider(t *testing.T) {
	cfg := appconfig.Config{Models: []appconfig.ModelProviderConfig{
		{Tag: "first", Type: appconfig.ModelProviderOpenAIChat, Model: "chat-1", APIKeyEnv: "FIRST_KEY"},
		{Tag: "second", Type: appconfig.ModelProviderOpenAIChat, Model: "chat-2", APIKeyEnv: "SECOND_KEY"},
		{Tag: "responses", Type: appconfig.ModelProviderOpenAIResponses, Model: "resp-1", APIKeyEnv: "RESP_KEY"},
	}}
	profile := appconfig.Profile{Tag: "primary", Answers: &appconfig.AnswerPolicy{
		Model: &appconfig.AnswerModelPolicy{Provider: "second", PromptVersion: "v1", Instruction: "answer", Timeout: "30s"},
	}}
	provider, apiKey, err := chatReplyProvider(cfg, profile, func(name string) (string, bool) {
		return "key-" + name, true
	})
	if err != nil {
		t.Fatalf("resolve provider: %v", err)
	}
	if provider.Tag != "second" || provider.Model != "chat-2" || apiKey != "key-SECOND_KEY" {
		t.Fatalf("provider=%#v apiKey=%q", provider, apiKey)
	}

	profile.Answers.Model.Provider = "responses"
	if _, _, err := chatReplyProvider(cfg, profile, func(string) (string, bool) { return "key", true }); err == nil {
		t.Fatal("expected a responses provider to be rejected for chat replies")
	}

	profile.Answers = nil
	provider, _, err = chatReplyProvider(cfg, profile, func(string) (string, bool) { return "key", true })
	if err != nil || provider.Tag != "first" {
		t.Fatalf("fallback provider=%#v err=%v", provider, err)
	}
	if _, _, err := chatReplyProvider(cfg, profile, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("expected a missing model key to fail")
	}
}

func TestConversationReplyPromptKeepsFactsAndHistory(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	conversation := core.Conversation{VacancyTitle: "DevOps Engineer", Employer: "Example"}
	history := []core.ConversationMessage{
		{Direction: core.MessageIncoming, Kind: core.MessageText, Text: "Какой у вас опыт?", OccurredAt: now},
		{Direction: core.MessageOutgoing, Kind: core.MessageText, Text: "Три года.", OccurredAt: now.Add(time.Minute)},
	}
	prompt := conversationReplyPrompt(conversation, "Какие облака использовали?", history, []byte(`{"clouds":"Yandex Cloud"}`))
	for _, expected := range []string{
		"DevOps Engineer", "Example", "ФАКТЫ (JSON)", `"clouds":"Yandex Cloud"`, "ВОПРОС:", "Работодатель:", "Кандидат:", "UNKNOWN",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt is missing %q:\n%s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "{{") {
		t.Fatalf("prompt leaked a placeholder: %s", prompt)
	}
}

func TestChatReplySalaryDetection(t *testing.T) {
	for _, question := range []string{
		"На какой уровень заработной платы вы ориентируетесь?",
		"Какие у вас ожидания по окладу?",
		"Какая ставка?",
		"Сколько вы хотите получать?",
	} {
		if !chatReplyIsSalaryQuestion(question) {
			t.Fatalf("salary question was not detected: %q", question)
		}
	}
	for _, question := range []string{"Какие технологии использовали?", "Готовы к тестовому?"} {
		if chatReplyIsSalaryQuestion(question) {
			t.Fatalf("non-salary question matched: %q", question)
		}
	}
}
