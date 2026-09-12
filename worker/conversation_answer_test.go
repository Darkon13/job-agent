package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func knownConversationAnswerRegistry(t *testing.T) *core.AnswerBlockRegistry {
	t.Helper()
	registry, err := core.NewAnswerBlockRegistry(core.AnswerBlock{
		Tag: "hr-education", Name: "Образование", Kind: core.AnswerBlockConversation, Platform: "hh",
		Match: core.AnswerBlockMatcher{Topic: "У Вас есть Высшее образование?"},
		Answers: []core.StoredAnswer{{
			Question:        "У Вас есть Высшее образование?",
			SelectedOptions: []string{"Да"},
		}},
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}

func conversationQuestionnairePrompt(now time.Time) core.ConversationMessage {
	return core.ConversationMessage{
		ID: "prompt-1", ConversationID: "conversation-1", ExternalID: "15447513328",
		Direction: core.MessageIncoming, Kind: core.MessageQuestionnaire, Status: core.MessageObserved,
		Text: "У Вас есть Высшее образование?",
		Options: []core.MessageOption{
			{ID: "15447513328:0", Text: "Да"},
			{ID: "15447513328:1", Text: "Нет"},
		},
		OccurredAt: now,
	}
}

func TestConversationSyncEnqueuesKnownQuestionnaireAnswer(t *testing.T) {
	ctx := context.Background()
	handlers, conversationWorkflow, repository, queue, transport, clock := newConversationHandlersFixture(t)
	handlers.ConfigureKnownAnswers(knownConversationAnswerRegistry(t), func(profileID core.ProfileID) bool {
		return profileID == "profile-1"
	})
	conversation, err := repository.Conversation(ctx, "conversation-1")
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	prompt := conversationQuestionnairePrompt(clock.now)
	if err := handlers.answerKnownQuestion(ctx, conversation, []core.ConversationMessage{prompt}); err != nil {
		t.Fatalf("answer known question: %v", err)
	}
	tasks := queue.Tasks()
	if len(tasks) != 1 || tasks[0].Type != core.TaskConversationSend {
		t.Fatalf("tasks = %#v", tasks)
	}
	var payload core.ConversationSendPayload
	if err := json.Unmarshal(tasks[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Content.Text != "Да" || payload.ReplyToID != "prompt-1" {
		t.Fatalf("payload = %#v", payload)
	}
	if err := handlers.answerKnownQuestion(ctx, conversation, []core.ConversationMessage{prompt}); err != nil {
		t.Fatalf("repeat answer known question: %v", err)
	}
	if len(queue.Tasks()) != 1 {
		t.Fatalf("repeat enqueued a duplicate: %#v", queue.Tasks())
	}
	if err := handlers.Send(ctx, tasks[0]); err != nil {
		t.Fatalf("send known answer: %v", err)
	}
	if len(transport.commands) != 1 || transport.commands[0].Text != "Да" || transport.commands[0].IdempotencyKey != tasks[0].IdempotencyKey {
		t.Fatalf("transport commands = %#v", transport.commands)
	}
	if _, _, err := conversationWorkflow.EnqueueMessage(ctx, "conversation-1", core.MessageContent{Text: "Да"}, "prompt-1", "questionnaire\x0015447513328\x0015447513328:0"); err != nil {
		t.Fatalf("re-enqueue sent answer: %v", err)
	}
	if len(queue.Tasks()) != 1 {
		t.Fatalf("re-enqueue created a duplicate: %#v", queue.Tasks())
	}
}

func TestConversationKnownAnswersRequirePolicy(t *testing.T) {
	ctx := context.Background()
	handlers, _, repository, queue, _, clock := newConversationHandlersFixture(t)
	handlers.ConfigureKnownAnswers(knownConversationAnswerRegistry(t), func(core.ProfileID) bool { return false })
	conversation, err := repository.Conversation(ctx, "conversation-1")
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	if err := handlers.answerKnownQuestion(ctx, conversation, []core.ConversationMessage{conversationQuestionnairePrompt(clock.now)}); err != nil {
		t.Fatalf("answer known question: %v", err)
	}
	if len(queue.Tasks()) != 0 {
		t.Fatalf("policy-disabled profile enqueued tasks: %#v", queue.Tasks())
	}
}
