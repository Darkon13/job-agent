package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func TestStorePersistsConversationsAndDueFollowUpsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 123, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	conversation, err := core.NewConversation("conversation-1", "hh", "profile-1", "external-chat-1", now)
	if err != nil {
		t.Fatalf("new conversation: %v", err)
	}
	if _, created, err := store.CreateConversation(ctx, conversation); err != nil || !created {
		t.Fatalf("create conversation: created=%t err=%v", created, err)
	}
	message := core.ConversationMessage{
		ID: "message-1", ConversationID: conversation.ID, ExternalID: "external-message-1",
		Direction: core.MessageOutgoing, Kind: core.MessageText, Status: core.MessageSent,
		Text: "Здравствуйте", OccurredAt: now.Add(time.Hour),
	}
	conversation, created, err := store.AppendConversationMessage(ctx, message, now.Add(time.Hour))
	if err != nil || !created {
		t.Fatalf("append message: created=%t err=%v", created, err)
	}
	if conversation.LastOutgoingAt == nil || !conversation.LastOutgoingAt.Equal(message.OccurredAt) {
		t.Fatalf("outgoing marker was not stored: %#v", conversation)
	}
	externalDuplicate := message
	externalDuplicate.ID = "message-runtime-copy"
	again, created, err := store.AppendConversationMessage(ctx, externalDuplicate, now.Add(2*time.Hour))
	if err != nil || created || again.Revision != conversation.Revision {
		t.Fatalf("deduplicate external message: conversation=%#v created=%t err=%v", again, created, err)
	}
	runAt := now.Add(73 * time.Hour)
	followUp, err := core.NewFollowUp(core.NewFollowUpParams{
		ID: "follow-up-1", ConversationID: conversation.ID, ProfileID: conversation.ProfileID,
		Platform: conversation.Platform, AnchorMessageID: message.ID, AnchorAt: message.OccurredAt,
		RunAt: runAt, Content: core.MessageContent{OperatorTag: "employer-reminder"},
		Policy:         core.FollowUpPolicy{CancelOnIncoming: true, RequireActiveConversation: true, MaxFollowUps: 1, Cooldown: core.Duration(72 * time.Hour)},
		IdempotencyKey: "conversation-1:message-1:reminder",
	}, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("new follow-up: %v", err)
	}
	if _, created, err := store.CreateFollowUp(ctx, followUp); err != nil || !created {
		t.Fatalf("create follow-up: created=%t err=%v", created, err)
	}
	conflict := followUp
	conflict.ID = "follow-up-runtime-copy"
	conflict.RunAt = conflict.RunAt.Add(time.Hour)
	if _, _, err := store.CreateFollowUp(ctx, conflict); err == nil {
		t.Fatal("expected conflicting follow-up idempotency key to fail")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	messages, err := store.ConversationMessages(ctx, conversation.ID)
	if err != nil || len(messages) != 1 || messages[0].Text != message.Text {
		t.Fatalf("reloaded messages: %#v err=%v", messages, err)
	}
	due, err := store.ListFollowUps(ctx, storage.FollowUpFilter{Status: core.FollowUpScheduled, DueBefore: &runAt})
	if err != nil || len(due) != 1 || due[0].Content.OperatorTag != "employer-reminder" || due[0].Policy.Cooldown.Value() != 72*time.Hour {
		t.Fatalf("reloaded due follow-ups: %#v err=%v", due, err)
	}
	stale := due[0]
	queued := due[0]
	if err := queued.Queue(runAt); err != nil {
		t.Fatalf("queue follow-up: %v", err)
	}
	if err := store.SaveFollowUp(ctx, queued, stale.Revision); err != nil {
		t.Fatalf("save queued follow-up: %v", err)
	}
	if err := stale.Cancel(core.FollowUpUserRequested, runAt.Add(time.Second)); err != nil {
		t.Fatalf("cancel stale follow-up: %v", err)
	}
	if err := store.SaveFollowUp(ctx, stale, 1); !errors.Is(err, storage.ErrRevisionConflict) {
		t.Fatalf("stale save error = %v, want revision conflict", err)
	}
	stats, err := store.Stats(ctx)
	if err != nil || stats.Conversations != 1 || stats.Messages != 1 || stats.FollowUps != 1 {
		t.Fatalf("conversation stats: %#v err=%v", stats, err)
	}
}
