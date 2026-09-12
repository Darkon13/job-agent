package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func TestConversationAndFollowUpPersistence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	repository := NewRepository()
	conversation, err := core.NewConversation("conversation-1", "hh", "profile-1", "external-chat-1", now)
	if err != nil {
		t.Fatalf("new conversation: %v", err)
	}
	stored, created, err := repository.CreateConversation(ctx, conversation)
	if err != nil || !created || stored.ID != conversation.ID {
		t.Fatalf("create conversation: stored=%#v created=%t err=%v", stored, created, err)
	}
	duplicate := conversation
	duplicate.ID = "conversation-2"
	stored, created, err = repository.CreateConversation(ctx, duplicate)
	if err != nil || created || stored.ID != conversation.ID {
		t.Fatalf("deduplicate conversation: stored=%#v created=%t err=%v", stored, created, err)
	}

	newer := core.ConversationMessage{
		ID: "message-2", ConversationID: conversation.ID, ExternalID: "external-message-2",
		Direction: core.MessageIncoming, Kind: core.MessageText, Status: core.MessageObserved,
		Text: "newer", OccurredAt: now.Add(2 * time.Hour),
	}
	stored, created, err = repository.AppendConversationMessage(ctx, newer, now.Add(2*time.Hour))
	if err != nil || !created {
		t.Fatalf("append newer message: created=%t err=%v", created, err)
	}
	older := newer
	older.ID = "message-1"
	older.ExternalID = "external-message-1"
	older.Text = "older"
	older.OccurredAt = now.Add(time.Hour)
	stored, created, err = repository.AppendConversationMessage(ctx, older, now.Add(3*time.Hour))
	if err != nil || !created {
		t.Fatalf("append historical message: created=%t err=%v", created, err)
	}
	if stored.LastMessageID != newer.ID || stored.LastIncomingAt == nil || !stored.LastIncomingAt.Equal(newer.OccurredAt) {
		t.Fatalf("historical message replaced latest markers: %#v", stored)
	}
	again, created, err := repository.AppendConversationMessage(ctx, older, now.Add(4*time.Hour))
	if err != nil || created || again.Revision != stored.Revision {
		t.Fatalf("deduplicate message: conversation=%#v created=%t err=%v", again, created, err)
	}
	externalDuplicate := older
	externalDuplicate.ID = "message-runtime-copy"
	again, created, err = repository.AppendConversationMessage(ctx, externalDuplicate, now.Add(4*time.Hour))
	if err != nil || created || again.Revision != stored.Revision {
		t.Fatalf("deduplicate external message: conversation=%#v created=%t err=%v", again, created, err)
	}
	messages, err := repository.ConversationMessages(ctx, conversation.ID)
	if err != nil || len(messages) != 2 || messages[0].ID != older.ID || messages[1].ID != newer.ID {
		t.Fatalf("message timeline: %#v err=%v", messages, err)
	}

	runAt := now.Add(72 * time.Hour)
	followUp, err := core.NewFollowUp(core.NewFollowUpParams{
		ID: "follow-up-1", ConversationID: conversation.ID, ProfileID: conversation.ProfileID,
		Platform: conversation.Platform, AnchorMessageID: newer.ID, AnchorAt: newer.OccurredAt,
		RunAt: runAt, Content: core.MessageContent{TemplateTag: "remind-employer"},
		Policy:         core.FollowUpPolicy{CancelOnIncoming: true, RequireActiveConversation: true, MaxFollowUps: 1, Cooldown: core.Duration(72 * time.Hour)},
		IdempotencyKey: "conversation-1:message-2:reminder",
	}, now.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("new follow-up: %v", err)
	}
	storedFollowUp, created, err := repository.CreateFollowUp(ctx, followUp)
	if err != nil || !created {
		t.Fatalf("create follow-up: created=%t err=%v", created, err)
	}
	duplicateFollowUp := followUp
	duplicateFollowUp.ID = "follow-up-2"
	existing, created, err := repository.CreateFollowUp(ctx, duplicateFollowUp)
	if err != nil || created || existing.ID != storedFollowUp.ID {
		t.Fatalf("deduplicate follow-up: stored=%#v created=%t err=%v", existing, created, err)
	}
	conflict := duplicateFollowUp
	conflict.RunAt = conflict.RunAt.Add(time.Hour)
	if _, _, err := repository.CreateFollowUp(ctx, conflict); err == nil {
		t.Fatal("expected conflicting follow-up idempotency key to fail")
	}
	due, err := repository.ListFollowUps(ctx, storage.FollowUpFilter{Status: core.FollowUpScheduled, DueBefore: &runAt})
	if err != nil || len(due) != 1 || due[0].ID != followUp.ID {
		t.Fatalf("due follow-ups: %#v err=%v", due, err)
	}
	stale := followUp
	if err := followUp.Queue(runAt); err != nil {
		t.Fatalf("queue follow-up: %v", err)
	}
	if err := repository.SaveFollowUp(ctx, followUp, 1); err != nil {
		t.Fatalf("save queued follow-up: %v", err)
	}
	if err := stale.Cancel(core.FollowUpUserRequested, runAt.Add(time.Second)); err != nil {
		t.Fatalf("cancel stale follow-up: %v", err)
	}
	if err := repository.SaveFollowUp(ctx, stale, 1); !errors.Is(err, storage.ErrRevisionConflict) {
		t.Fatalf("stale save error = %v, want revision conflict", err)
	}
}

func TestAppendConversationMessageDeduplicatesSubMillisecondDrift(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 10, 50, 0, 539773913, time.UTC)
	repository := NewRepository()
	conversation, err := core.NewConversation("conversation-1", "hh", "primary", "external-chat-1", now)
	if err != nil {
		t.Fatalf("new conversation: %v", err)
	}
	if _, _, err := repository.CreateConversation(ctx, conversation); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	sent := core.ConversationMessage{
		ID: "message-hh-1", ConversationID: "conversation-1", ExternalID: "15449574008",
		Direction: core.MessageOutgoing, Kind: core.MessageText, Status: core.MessageSent,
		Text: "Посмотрю вакансию, спасибо", OccurredAt: now,
	}
	if _, created, err := repository.AppendConversationMessage(ctx, sent, now); err != nil || !created {
		t.Fatalf("append sent message: created=%t err=%v", created, err)
	}
	sent.ReplyToID = "message-prompt"
	if _, created, err := repository.AppendConversationMessage(ctx, sent, now); err != nil || created {
		t.Fatalf("append sent message with reply: created=%t err=%v", created, err)
	}
	synced := sent
	synced.ReplyToID = ""
	synced.OccurredAt = now.Truncate(time.Millisecond)
	if _, created, err := repository.AppendConversationMessage(ctx, synced, now.Add(time.Minute)); err != nil || created {
		t.Fatalf("expected sync deduplication: created=%t err=%v", created, err)
	}
}
