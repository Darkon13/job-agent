package core

import (
	"encoding/json"
	"testing"
	"time"
)

func testConversation(t *testing.T, now time.Time) Conversation {
	t.Helper()
	conversation, err := NewConversation("conversation-1", "hh", "profile-1", "external-chat-1", now)
	if err != nil {
		t.Fatalf("new conversation: %v", err)
	}
	return conversation
}

func testFollowUp(t *testing.T, now time.Time) FollowUp {
	t.Helper()
	followUp, err := NewFollowUp(NewFollowUpParams{
		ID: "follow-up-1", ConversationID: "conversation-1", ProfileID: "profile-1", Platform: "hh",
		AnchorMessageID: "outgoing-1", AnchorAt: now, RunAt: now.Add(72 * time.Hour),
		Content: MessageContent{TemplateTag: "remind-employer"},
		Policy: FollowUpPolicy{
			CancelOnIncoming: true, RequireActiveConversation: true,
			MaxFollowUps: 1, Cooldown: Duration(72 * time.Hour),
		},
		IdempotencyKey: "follow-up:conversation-1:outgoing-1",
	}, now)
	if err != nil {
		t.Fatalf("new follow-up: %v", err)
	}
	return followUp
}

func TestFollowUpQueuesOnlyWhenDueAndSendsOnce(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	followUp := testFollowUp(t, now)
	originalRevision := followUp.Revision
	if err := followUp.Queue(now.Add(time.Hour)); err == nil {
		t.Fatal("expected an early follow-up to stay scheduled")
	}
	if followUp.Status != FollowUpScheduled || followUp.Revision != originalRevision {
		t.Fatalf("early queue mutated follow-up: %#v", followUp)
	}
	dueAt := now.Add(72 * time.Hour)
	if err := followUp.Queue(dueAt); err != nil {
		t.Fatalf("queue due follow-up: %v", err)
	}
	if err := followUp.MarkSent("message-2", dueAt.Add(time.Second)); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if err := followUp.MarkSent("message-3", dueAt.Add(2*time.Second)); err == nil {
		t.Fatal("expected terminal follow-up not to send twice")
	}
}

func TestIncomingMessageCancelsPendingFollowUp(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	conversation := testConversation(t, now)
	followUp := testFollowUp(t, now)
	incomingAt := now.Add(24 * time.Hour)
	changed, err := conversation.Observe(ConversationMessage{
		ID: "incoming-1", ConversationID: conversation.ID, ExternalID: "external-message-1",
		Direction: MessageIncoming, Kind: MessageText, Status: MessageObserved,
		Text: "Давайте созвонимся", OccurredAt: incomingAt,
	}, incomingAt)
	if err != nil || !changed {
		t.Fatalf("observe incoming message: changed=%t err=%v", changed, err)
	}
	reason, cancel, err := followUp.CancellationFor(conversation)
	if err != nil || !cancel || reason != FollowUpIncomingReceived {
		t.Fatalf("unexpected cancellation decision: reason=%s cancel=%t err=%v", reason, cancel, err)
	}
	if err := followUp.Cancel(reason, incomingAt); err != nil {
		t.Fatalf("cancel follow-up: %v", err)
	}
}

func TestInactiveConversationCancelsFollowUp(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	conversation := testConversation(t, now)
	followUp := testFollowUp(t, now)
	if err := conversation.SetStatus(ConversationRejected, now.Add(time.Hour)); err != nil {
		t.Fatalf("reject conversation: %v", err)
	}
	reason, cancel, err := followUp.CancellationFor(conversation)
	if err != nil || !cancel || reason != FollowUpConversationInactive {
		t.Fatalf("unexpected cancellation decision: reason=%s cancel=%t err=%v", reason, cancel, err)
	}
}

func TestConversationTracksUnreadCatalogStateAndMarkRead(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	conversation := testConversation(t, now)
	changed, err := conversation.ObserveCatalogState(ConversationActive, 3, now.Add(time.Minute))
	if err != nil || !changed || conversation.UnreadCount != 3 || conversation.Revision != 2 {
		t.Fatalf("observe unread state: conversation=%#v changed=%t err=%v", conversation, changed, err)
	}
	changed, err = conversation.MarkRead(now.Add(2 * time.Minute))
	if err != nil || !changed || conversation.UnreadCount != 0 || conversation.Revision != 3 {
		t.Fatalf("mark read: conversation=%#v changed=%t err=%v", conversation, changed, err)
	}
	changed, err = conversation.MarkRead(now.Add(3 * time.Minute))
	if err != nil || changed || conversation.Revision != 3 {
		t.Fatalf("repeat mark read: conversation=%#v changed=%t err=%v", conversation, changed, err)
	}
}

func TestConversationContentRequiresOneSource(t *testing.T) {
	if err := (MessageContent{}).Validate(); err == nil {
		t.Fatal("expected empty content to fail")
	}
	if err := (MessageContent{Text: "hello", OperatorTag: "writer"}).Validate(); err == nil {
		t.Fatal("expected ambiguous content to fail")
	}
	if err := (MessageContent{OperatorTag: "writer"}).Validate(); err != nil {
		t.Fatalf("validate operator content: %v", err)
	}
}

func TestConversationSendIdempotencyIsScopedAndStable(t *testing.T) {
	first, err := ConversationSendIdempotencyKey("conversation-1", "request-1")
	if err != nil {
		t.Fatalf("first key: %v", err)
	}
	again, _ := ConversationSendIdempotencyKey("conversation-1", "request-1")
	other, _ := ConversationSendIdempotencyKey("conversation-2", "request-1")
	if first != again || first == other {
		t.Fatalf("unexpected keys: first=%q again=%q other=%q", first, again, other)
	}
}

func TestFollowUpPolicyUsesReadableJSONDuration(t *testing.T) {
	encoded, err := json.Marshal(FollowUpPolicy{MaxFollowUps: 1, Cooldown: Duration(72 * time.Hour)})
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	if string(encoded) != `{"cancel_on_incoming":false,"require_active_conversation":false,"max_follow_ups":1,"cooldown":"72h0m0s"}` {
		t.Fatalf("unexpected policy JSON: %s", encoded)
	}
	var decoded FollowUpPolicy
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal policy: %v", err)
	}
	if decoded.Cooldown.Value() != 72*time.Hour {
		t.Fatalf("unexpected cooldown: %s", decoded.Cooldown.Value())
	}
}

func TestConversationFollowUpSelectionRequiresSafePolicy(t *testing.T) {
	payload := ConversationFollowUpSelectPayload{
		ProfileID: "primary", Strategy: FollowUpSelectOldestUnanswered,
		MinimumSilence: Duration(72 * time.Hour), RunAfter: Duration(time.Minute), DeadlineAfter: Duration(24 * time.Hour),
		Content: MessageContent{Text: "Подскажите, вакансия ещё актуальна?"},
		Policy: FollowUpPolicy{
			CancelOnIncoming: true, RequireActiveConversation: true,
			MaxFollowUps: 1, Cooldown: Duration(72 * time.Hour),
		},
	}
	if err := payload.Validate(); err != nil {
		t.Fatalf("validate selection payload: %v", err)
	}
	payload.Policy.CancelOnIncoming = false
	if err := payload.Validate(); err == nil {
		t.Fatal("expected selected follow-up without incoming cancellation to fail")
	}
}
