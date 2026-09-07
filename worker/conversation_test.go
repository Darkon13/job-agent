package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
	"github.com/Darkon13/job-agent/workflow"
)

type conversationClock struct{ now time.Time }

func (clock *conversationClock) Now() time.Time { return clock.now }

type conversationIDs struct{ next int }

func (ids *conversationIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}

type fakeConversationTransport struct {
	now      time.Time
	commands []adapter.ConversationSendCommand
}

func (transport *fakeConversationTransport) SendConversationMessage(_ context.Context, command adapter.ConversationSendCommand) (core.ConversationMessage, error) {
	transport.commands = append(transport.commands, command)
	return core.ConversationMessage{
		ID: core.MessageID(fmt.Sprintf("sent-%d", len(transport.commands))), ConversationID: command.ConversationID,
		ExternalID: fmt.Sprintf("external-sent-%d", len(transport.commands)), Direction: core.MessageOutgoing,
		Kind: core.MessageText, Status: core.MessageSent, Text: command.Text, OccurredAt: transport.now,
	}, nil
}

func (*fakeConversationTransport) MarkConversationRead(context.Context, core.ProfileID, string) error {
	return nil
}

func (transport *fakeConversationTransport) SyncConversation(context.Context, core.ProfileID, core.ConversationID, string) (adapter.ConversationSyncResult, error) {
	return adapter.ConversationSyncResult{ObservedAt: transport.now}, nil
}

func newConversationHandlersFixture(t *testing.T) (*ConversationHandlers, *workflow.ConversationWorkflow, *storagememory.Repository, *brokermemory.Queue, *fakeConversationTransport, *conversationClock) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	conversation, err := core.NewConversation("conversation-1", "hh", "profile-1", "external-chat-1", now)
	if err != nil {
		t.Fatalf("new conversation: %v", err)
	}
	if _, _, err := repository.CreateConversation(ctx, conversation); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	queue := brokermemory.NewQueue()
	clock := &conversationClock{now: now}
	conversationWorkflow, err := workflow.NewConversationWorkflow(repository, queue, clock, &conversationIDs{})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	transport := &fakeConversationTransport{now: now}
	transports := NewConversationTransportRegistry()
	if err := transports.Register("profile-1", transport); err != nil {
		t.Fatalf("register transport: %v", err)
	}
	handlers, err := NewConversationHandlers(repository, repository, conversationWorkflow, transports, StaticMessageResolver{}, clock)
	if err != nil {
		t.Fatalf("new handlers: %v", err)
	}
	return handlers, conversationWorkflow, repository, queue, transport, clock
}

func TestConversationSendHandlerCallsTransportAndStoresMessage(t *testing.T) {
	ctx := context.Background()
	handlers, conversationWorkflow, repository, _, transport, _ := newConversationHandlersFixture(t)
	task, _, err := conversationWorkflow.EnqueueMessage(ctx, "conversation-1", core.MessageContent{Text: "Здравствуйте"}, "", "send-1")
	if err != nil {
		t.Fatalf("enqueue message: %v", err)
	}
	if err := handlers.Send(ctx, task); err != nil {
		t.Fatalf("handle send: %v", err)
	}
	messages, err := repository.ConversationMessages(ctx, "conversation-1")
	if err != nil || len(messages) != 1 || messages[0].Text != "Здравствуйте" || len(transport.commands) != 1 {
		t.Fatalf("stored messages=%#v commands=%#v err=%v", messages, transport.commands, err)
	}
	if transport.commands[0].IdempotencyKey != task.IdempotencyKey {
		t.Fatalf("transport did not receive idempotency key: %#v", transport.commands[0])
	}
	activity, err := repository.ListProfileActivity(ctx, storage.ProfileActivityFilter{ProfileID: "profile-1"})
	if err != nil || len(activity) != 1 || activity[0].Kind != core.ProfileActivityConversationMessageSent || activity[0].SourceID != string(messages[0].ID) {
		t.Fatalf("conversation activity=%#v err=%v", activity, err)
	}
}

func TestConversationFollowUpHandlerCompletesLifecycle(t *testing.T) {
	ctx := context.Background()
	handlers, conversationWorkflow, repository, _, transport, clock := newConversationHandlersFixture(t)
	runAt := clock.now.Add(time.Hour)
	followUp, _, err := conversationWorkflow.ScheduleFollowUp(ctx, workflow.ScheduleFollowUpRequest{
		ConversationID: "conversation-1", AnchorMessageID: "anchor-1", AnchorAt: clock.now,
		RunAt: runAt, Content: core.MessageContent{Text: "Напоминаю о себе"},
		Policy:         core.FollowUpPolicy{CancelOnIncoming: true, RequireActiveConversation: true, MaxFollowUps: 1},
		IdempotencyKey: "follow-up-1",
	})
	if err != nil {
		t.Fatalf("schedule follow-up: %v", err)
	}
	clock.now = runAt
	transport.now = runAt
	if _, err := conversationWorkflow.ReconcileDueFollowUps(ctx, runAt); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	task := followUpTask(t, followUp)
	if err := handlers.FollowUp(ctx, task); err != nil {
		t.Fatalf("handle follow-up: %v", err)
	}
	stored, err := repository.FollowUp(ctx, followUp.ID)
	if err != nil || stored.Status != core.FollowUpSent || stored.SentMessageID == "" || len(transport.commands) != 1 {
		t.Fatalf("stored follow-up=%#v commands=%#v err=%v", stored, transport.commands, err)
	}
}

func followUpTask(t *testing.T, followUp core.FollowUp) core.Task {
	t.Helper()
	key, err := core.ConversationFollowUpIdempotencyKey(followUp.ID)
	if err != nil {
		t.Fatalf("follow-up key: %v", err)
	}
	// The workflow queue is intentionally hidden; build the same immutable task
	// envelope to test the handler boundary directly.
	payload := []byte(fmt.Sprintf(`{"follow_up_id":%q}`, followUp.ID))
	task, err := core.NewTask(core.NewTaskParams{
		ID: "follow-up-task", Type: core.TaskConversationFollowUp, IdempotencyKey: key,
		Source: "test", Platform: followUp.Platform, ProfileID: followUp.ProfileID,
		CorrelationID: "correlation-1", Payload: payload, AvailableAt: followUp.RunAt,
	}, followUp.RunAt)
	if err != nil {
		t.Fatalf("new follow-up task: %v", err)
	}
	return task
}
