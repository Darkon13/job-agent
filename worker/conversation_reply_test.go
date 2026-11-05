package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

func conversationSyncTask(t *testing.T, now time.Time, id string) core.Task {
	t.Helper()
	payload, err := json.Marshal(core.ConversationIDPayload{ConversationID: "conversation-1"})
	if err != nil {
		t.Fatalf("encode sync payload: %v", err)
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(id), Type: core.TaskConversationSync, IdempotencyKey: id,
		Source: "test", Platform: "hh", ProfileID: "profile-1", CorrelationID: "correlation-sync",
		Payload: payload, AvailableAt: now,
	}, now)
	if err != nil {
		t.Fatalf("new sync task: %v", err)
	}
	return task
}

func registerReplyGenerator(t *testing.T, handlers *ConversationHandlers, answer string, calls *int) {
	t.Helper()
	registry := NewConversationReplyRegistry()
	if err := registry.Register("profile-1", func(_ context.Context, _ core.Conversation, _ core.ConversationMessage, _ []core.ConversationMessage) (string, error) {
		if calls != nil {
			*calls = *calls + 1
		}
		return answer, nil
	}); err != nil {
		t.Fatalf("register reply generator: %v", err)
	}
	handlers.ConfigureAutoReplies(registry)
}

func replyTaskFromQueue(t *testing.T, tasks []core.Task) core.Task {
	t.Helper()
	for _, task := range tasks {
		if task.Type == core.TaskConversationReply {
			return task
		}
	}
	t.Fatalf("reply task not found in %#v", tasks)
	return core.Task{}
}

func TestConversationSyncEnqueuesAutoReplyForTextQuestion(t *testing.T) {
	ctx := context.Background()
	handlers, _, _, queue, transport, clock := newConversationHandlersFixture(t)
	generatorCalls := 0
	registerReplyGenerator(t, handlers, "Да, готов обсудить детали.", &generatorCalls)
	transport.syncResult = &adapter.ConversationSyncResult{
		ObservedAt: clock.now,
		Messages: []core.ConversationMessage{{
			ID: "prompt-1", ConversationID: "conversation-1", ExternalID: "external-prompt-1",
			Direction: core.MessageIncoming, Kind: core.MessageText, Status: core.MessageObserved,
			Text: "Готовы ли вы обсудить детали сейчас в чате?", OccurredAt: clock.now,
		}},
	}
	syncTask := conversationSyncTask(t, clock.now, "sync-auto-1")
	if err := handlers.Sync(ctx, syncTask); err != nil {
		t.Fatalf("sync conversations: %v", err)
	}
	replyTask := replyTaskFromQueue(t, queue.Tasks())
	if generatorCalls != 0 {
		t.Fatalf("sync must not call the model: calls=%d", generatorCalls)
	}
	var payload core.ConversationReplyPayload
	if err := json.Unmarshal(replyTask.Payload, &payload); err != nil {
		t.Fatalf("decode reply payload: %v", err)
	}
	if payload.ConversationID != "conversation-1" || payload.PromptMessageID != "prompt-1" || payload.PromptExternalID != "external-prompt-1" {
		t.Fatalf("reply payload = %#v", payload)
	}
	if err := handlers.Sync(ctx, syncTask); err != nil {
		t.Fatalf("repeat sync: %v", err)
	}
	if len(queue.Tasks()) != 1 {
		t.Fatalf("repeat sync created a duplicate reply task: %#v", queue.Tasks())
	}
	if err := handlers.Reply(ctx, replyTask); err != nil {
		t.Fatalf("handle reply: %v", err)
	}
	if generatorCalls != 1 {
		t.Fatalf("generator calls = %d", generatorCalls)
	}
	if len(transport.commands) != 1 || transport.commands[0].Text != "Да, готов обсудить детали." {
		t.Fatalf("transport commands = %#v", transport.commands)
	}
	if transport.commands[0].IdempotencyKey != replyTask.IdempotencyKey || transport.commands[0].ReplyToID != "prompt-1" {
		t.Fatalf("transport command did not use the reply task identity: %#v", transport.commands[0])
	}
	// The reply task itself is completed outside the handler; a repeated sync
	// after the outgoing message is stored must not enqueue another reply.
	if err := handlers.Sync(ctx, syncTask); err != nil {
		t.Fatalf("sync after reply: %v", err)
	}
	if len(queue.Tasks()) != 1 {
		t.Fatalf("sync after reply created another task: %#v", queue.Tasks())
	}
}

func TestConversationAutoReplySkipsDisqualifyingAndButtonPrompts(t *testing.T) {
	for name, message := range map[string]core.ConversationMessage{
		"disqualifier": {
			ID: "prompt-1", ConversationID: "conversation-1", ExternalID: "external-1",
			Direction: core.MessageIncoming, Kind: core.MessageText, Status: core.MessageObserved,
			Text: "Готовы к переезду в Москву?", OccurredAt: time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC),
		},
		"acknowledgement": {
			ID: "prompt-1", ConversationID: "conversation-1", ExternalID: "external-1",
			Direction: core.MessageIncoming, Kind: core.MessageText, Status: core.MessageObserved,
			Text: "Спасибо, всё понятно", OccurredAt: time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC),
		},
		"button questionnaire": {
			ID: "prompt-1", ConversationID: "conversation-1", ExternalID: "external-1",
			Direction: core.MessageIncoming, Kind: core.MessageQuestionnaire, Status: core.MessageObserved,
			Text: "Есть ли опыт с Kafka?", Options: []core.MessageOption{{ID: "yes", Text: "Да"}, {ID: "no", Text: "Нет"}},
			OccurredAt: time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC),
		},
		"system": {
			ID: "prompt-1", ConversationID: "conversation-1", ExternalID: "external-1",
			Direction: core.MessageIncoming, Kind: core.MessageSystem, Status: core.MessageObserved,
			Text: "Событие переговоров HH (PARTICIPANT_LEFT)", OccurredAt: time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC),
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			handlers, _, _, queue, transport, clock := newConversationHandlersFixture(t)
			registerReplyGenerator(t, handlers, "Ответ", nil)
			message := message
			message.OccurredAt = clock.now
			transport.syncResult = &adapter.ConversationSyncResult{ObservedAt: clock.now, Messages: []core.ConversationMessage{message}}
			if err := handlers.Sync(ctx, conversationSyncTask(t, clock.now, "sync-skip")); err != nil {
				t.Fatalf("sync conversations: %v", err)
			}
			if len(queue.Tasks()) != 0 {
				t.Fatalf("enqueued a reply for %s: %#v", name, queue.Tasks())
			}
		})
	}
}

func TestConversationReplyHandlerRechecksNewerIncoming(t *testing.T) {
	ctx := context.Background()
	handlers, conversationWorkflow, repository, _, transport, clock := newConversationHandlersFixture(t)
	registerReplyGenerator(t, handlers, "Да.", nil)
	prompt := core.ConversationMessage{
		ID: "prompt-1", ConversationID: "conversation-1", ExternalID: "external-prompt-1",
		Direction: core.MessageIncoming, Kind: core.MessageText, Status: core.MessageObserved,
		Text: "Готовы обсудить детали?", OccurredAt: clock.now,
	}
	if _, _, err := repository.AppendConversationMessage(ctx, prompt, clock.now); err != nil {
		t.Fatalf("append prompt: %v", err)
	}
	replyTask, _, err := conversationWorkflow.EnqueueReply(ctx, core.ConversationReplyPayload{
		ConversationID: "conversation-1", PromptMessageID: prompt.ID, PromptExternalID: prompt.ExternalID,
	}, prompt.ExternalID)
	if err != nil {
		t.Fatalf("enqueue reply: %v", err)
	}
	clock.now = clock.now.Add(time.Minute)
	newer := core.ConversationMessage{
		ID: "prompt-2", ConversationID: "conversation-1", ExternalID: "external-prompt-2",
		Direction: core.MessageIncoming, Kind: core.MessageText, Status: core.MessageObserved,
		Text: "Спасибо, ожидайте ответа.", OccurredAt: clock.now,
	}
	if _, _, err := repository.AppendConversationMessage(ctx, newer, clock.now); err != nil {
		t.Fatalf("append newer message: %v", err)
	}
	if err := handlers.Reply(ctx, replyTask); err != nil {
		t.Fatalf("handle stale reply: %v", err)
	}
	if len(transport.commands) != 0 {
		t.Fatalf("stale prompt received a reply: %#v", transport.commands)
	}
}

func TestNormalizeAutoReply(t *testing.T) {
	tests := []struct {
		name string
		text string
		ok   bool
	}{
		{"plain", "Да, готов обсудить детали.", true},
		{"empty", "   ", false},
		{"unknown", "UNKNOWN", false},
		{"placeholder", "Ожидания {{salary}}", false},
		{"code fence", "```json\n{}\n```", false},
		{"json", `{"text":"нет"}`, false},
		{"email", "Пишите на user@example.test", false},
		{"link", "Профиль: https://example.test", false},
		{"too long", strings.Repeat("я", conversationReplyMaximumRunes+1), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text, ok := normalizeAutoReply(test.text)
			if ok != test.ok {
				t.Fatalf("normalize(%q) = %q, %t; want ok=%t", test.text, text, ok, test.ok)
			}
			if ok && strings.TrimSpace(text) == "" {
				t.Fatal("accepted an empty normalized answer")
			}
		})
	}
}

func TestConversationAutoReplySkipsStalePrompt(t *testing.T) {
	ctx := context.Background()
	handlers, _, _, queue, transport, clock := newConversationHandlersFixture(t)
	registerReplyGenerator(t, handlers, "Ответ", nil)
	transport.syncResult = &adapter.ConversationSyncResult{
		ObservedAt: clock.now,
		Messages: []core.ConversationMessage{{
			ID: "prompt-old", ConversationID: "conversation-1", ExternalID: "external-old",
			Direction: core.MessageIncoming, Kind: core.MessageText, Status: core.MessageObserved,
			Text: "Готовы обсудить детали?", OccurredAt: clock.now.Add(-8 * 24 * time.Hour),
		}},
	}
	if err := handlers.Sync(ctx, conversationSyncTask(t, clock.now, "sync-stale")); err != nil {
		t.Fatalf("sync conversations: %v", err)
	}
	if len(queue.Tasks()) != 0 {
		t.Fatalf("stale prompt produced a reply task: %#v", queue.Tasks())
	}
}
