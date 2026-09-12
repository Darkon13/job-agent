package core

import (
	"testing"
	"time"
)

func conversationAnswerRegistry(t *testing.T, block AnswerBlock) *AnswerBlockRegistry {
	t.Helper()
	registry, err := NewAnswerBlockRegistry(block)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}

func conversationPrompt(occurredAt time.Time) ConversationMessage {
	return ConversationMessage{
		ID: "prompt-1", ConversationID: "conversation-1", ExternalID: "15447513328",
		Direction: MessageIncoming, Kind: MessageQuestionnaire, Status: MessageObserved,
		Text: "У Вас есть Высшее образование?",
		Options: []MessageOption{
			{ID: "15447513328:0", Text: "Да"},
			{ID: "15447513328:1", Text: "Нет"},
		},
		OccurredAt: occurredAt,
	}
}

func TestResolveKnownConversationAnswerSelectsReviewedOption(t *testing.T) {
	now := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	registry := conversationAnswerRegistry(t, AnswerBlock{
		Tag: "hr-education", Name: "Образование", Kind: AnswerBlockConversation, Platform: "hh",
		Match: AnswerBlockMatcher{Topic: "у вас есть высшее образование?"},
		Answers: []StoredAnswer{{
			Question:        "У Вас есть Высшее образование?",
			SelectedOptions: []string{"да"},
		}},
	})
	decision, found := ResolveKnownConversationAnswer("hh", []ConversationMessage{conversationPrompt(now)}, registry)
	if !found {
		t.Fatal("expected a known conversation answer")
	}
	if decision.PromptMessageID != "prompt-1" || decision.PromptExternalID != "15447513328" ||
		decision.Option.ID != "15447513328:0" || decision.Option.Text != "Да" || decision.AnswerBlockTag != "hr-education" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestResolveKnownConversationAnswerSkipsAnsweredAndUnknownPrompts(t *testing.T) {
	now := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	registry := conversationAnswerRegistry(t, AnswerBlock{
		Tag: "hr-education", Name: "Образование", Kind: AnswerBlockConversation, Platform: "hh",
		Match: AnswerBlockMatcher{Topic: "У Вас есть Высшее образование?"},
		Answers: []StoredAnswer{{
			Question:        "У Вас есть Высшее образование?",
			SelectedOptions: []string{"Да"},
		}},
	})
	answered := conversationPrompt(now)
	reply := ConversationMessage{
		ID: "outgoing-1", ConversationID: "conversation-1", Direction: MessageOutgoing,
		Kind: MessageText, Status: MessageSent, Text: "Да", OccurredAt: now.Add(time.Minute),
	}
	if _, found := ResolveKnownConversationAnswer("hh", []ConversationMessage{answered, reply}, registry); found {
		t.Fatal("answered prompt must not resolve again")
	}
	stale := conversationPrompt(now)
	stale.Options = []MessageOption{{ID: "15447513328:0", Text: "Наверное"}}
	if _, found := ResolveKnownConversationAnswer("hh", []ConversationMessage{stale}, registry); found {
		t.Fatal("a stale option set must not resolve")
	}
	unknown := conversationPrompt(now)
	unknown.Text = "Какая у вас зарплата?"
	if _, found := ResolveKnownConversationAnswer("hh", []ConversationMessage{unknown}, registry); found {
		t.Fatal("an unknown question must not resolve")
	}
}

func TestResolveKnownConversationAnswerPicksEarliestUnansweredAndMatchesFingerprint(t *testing.T) {
	now := time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC)
	first := conversationPrompt(now)
	first.ExternalID = "15447513001"
	first.ID = "prompt-first"
	second := conversationPrompt(now.Add(2 * time.Minute))
	second.ExternalID = "15447513002"
	second.ID = "prompt-second"
	question := Question{ID: first.ExternalID, Text: first.Text, Kind: QuestionSingle, Options: []QuestionOption{
		{ID: "15447513328:0", Text: "Да"}, {ID: "15447513328:1", Text: "Нет"},
	}}
	fingerprint, err := QuestionFingerprint(question)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	registry := conversationAnswerRegistry(t, AnswerBlock{
		Tag: "hr-education", Name: "Образование", Kind: AnswerBlockConversation, Platform: "hh",
		Match: AnswerBlockMatcher{Fingerprint: fingerprint},
		Answers: []StoredAnswer{{
			Question:            "У Вас есть Высшее образование?",
			QuestionFingerprint: fingerprint,
			SelectedOptions:     []string{"Да"},
		}},
	})
	decision, found := ResolveKnownConversationAnswer("hh", []ConversationMessage{second, first}, registry)
	if !found || decision.PromptMessageID != "prompt-first" {
		t.Fatalf("decision = %#v found=%t", decision, found)
	}
	freeText := conversationAnswerRegistry(t, AnswerBlock{
		Tag: "hr-education", Name: "Образование", Kind: AnswerBlockConversation, Platform: "hh",
		Match:   AnswerBlockMatcher{Fingerprint: fingerprint},
		Answers: []StoredAnswer{{Question: "У Вас есть Высшее образование?", Text: "Да"}},
	})
	if _, found := ResolveKnownConversationAnswer("hh", []ConversationMessage{first}, freeText); found {
		t.Fatal("a free-text conversation answer must not be sent as a button reply")
	}
}
