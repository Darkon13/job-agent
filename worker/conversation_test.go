package worker

import (
	"context"
	"encoding/json"
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

func TestConversationFollowUpSelectionHandlerSchedulesEligibleReminder(t *testing.T) {
	ctx := context.Background()
	_, conversationWorkflow, repository, _, _, clock := newConversationHandlersFixture(t)
	outgoing := core.ConversationMessage{
		ID: "outgoing-1", ConversationID: "conversation-1", Direction: core.MessageOutgoing,
		Kind: core.MessageText, Status: core.MessageSent, Text: "Буду рад обратной связи", OccurredAt: clock.now,
	}
	if _, _, err := repository.AppendConversationMessage(ctx, outgoing, clock.now); err != nil {
		t.Fatalf("append outgoing: %v", err)
	}
	clock.now = clock.now.Add(73 * time.Hour)
	handler, err := NewConversationFollowUpSelectionHandler(conversationWorkflow)
	if err != nil {
		t.Fatalf("new selection handler: %v", err)
	}
	payload, _ := json.Marshal(core.ConversationFollowUpSelectPayload{
		ProfileID: "profile-1", Strategy: core.FollowUpSelectOldestUnanswered,
		MinimumSilence: core.Duration(72 * time.Hour), RunAfter: core.Duration(time.Minute),
		Content: core.MessageContent{Text: "Подскажите, вакансия ещё актуальна?"},
		Policy: core.FollowUpPolicy{
			CancelOnIncoming: true, RequireActiveConversation: true,
			MaxFollowUps: 1, Cooldown: core.Duration(72 * time.Hour),
		},
	})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "selection-task", Type: core.TaskConversationFollowUpSelect, IdempotencyKey: "selection-run-1",
		Source: "test", Platform: "hh", ProfileID: "profile-1", CorrelationID: "correlation-selection", Payload: payload,
	}, clock.now)
	if err != nil {
		t.Fatalf("new selection task: %v", err)
	}
	if err := handler.Handle(ctx, task); err != nil {
		t.Fatalf("handle selection: %v", err)
	}
	followUps, err := repository.ListFollowUps(ctx, storage.FollowUpFilter{ProfileID: "profile-1"})
	if err != nil || len(followUps) != 1 || followUps[0].ConversationID != "conversation-1" {
		t.Fatalf("follow-ups=%#v err=%v", followUps, err)
	}
}

type fakeConversationTransport struct {
	now       time.Time
	commands  []adapter.ConversationSendCommand
	discovery adapter.ConversationDiscoveryResult
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

func (transport *fakeConversationTransport) DiscoverConversations(context.Context, core.ProfileID) (adapter.ConversationDiscoveryResult, error) {
	return transport.discovery, nil
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

func TestConversationMarkReadHandlerClearsStoredUnreadCount(t *testing.T) {
	ctx := context.Background()
	handlers, conversationWorkflow, repository, _, _, clock := newConversationHandlersFixture(t)
	conversation, err := repository.Conversation(ctx, "conversation-1")
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	expectedRevision := conversation.Revision
	if changed, err := conversation.ObserveCatalogState(core.ConversationActive, 2, clock.now); err != nil || !changed {
		t.Fatalf("set unread count: changed=%t err=%v", changed, err)
	}
	if err := repository.SaveConversation(ctx, conversation, expectedRevision); err != nil {
		t.Fatalf("save unread count: %v", err)
	}
	clock.now = clock.now.Add(time.Second)
	task, _, err := conversationWorkflow.EnqueueMarkRead(ctx, conversation.ID, "mark-all-1")
	if err != nil {
		t.Fatalf("enqueue mark read: %v", err)
	}
	if err := handlers.MarkRead(ctx, task); err != nil {
		t.Fatalf("handle mark read: %v", err)
	}
	stored, err := repository.Conversation(ctx, conversation.ID)
	if err != nil || stored.UnreadCount != 0 {
		t.Fatalf("stored conversation=%#v err=%v", stored, err)
	}
}

func TestConversationDiscoverHandlerStoresCatalogAndSchedulesFullSync(t *testing.T) {
	ctx := context.Background()
	handlers, _, repository, queue, transport, clock := newConversationHandlersFixture(t)
	transport.discovery = adapter.ConversationDiscoveryResult{
		ObservedAt: clock.now,
		Conversations: []core.ConversationObservation{
			{
				ExternalID: "external-chat-1", Status: core.ConversationActive, UnreadCount: 2,
				LastMessage: &core.ConversationMessageObservation{
					ExternalID: "message-external-1", Direction: core.MessageIncoming,
					Kind: core.MessageText, Text: "Добрый день", OccurredAt: clock.now.Add(-time.Minute),
				},
			},
			{ExternalID: "external-chat-2", Status: core.ConversationActive, UnreadCount: 1},
		},
	}
	payload, _ := json.Marshal(core.ConversationDiscoverPayload{ProfileID: "profile-1"})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "discover-task", Type: core.TaskConversationDiscover, IdempotencyKey: "discover-run-1",
		Source: "test", Platform: "hh", ProfileID: "profile-1", CorrelationID: "correlation-discover", Payload: payload,
	}, clock.now)
	if err != nil {
		t.Fatalf("new discovery task: %v", err)
	}
	if err := handlers.Discover(ctx, task); err != nil {
		t.Fatalf("discover conversations: %v", err)
	}
	conversations, err := repository.ListConversations(ctx, storage.ConversationFilter{ProfileID: "profile-1"})
	if err != nil || len(conversations) != 2 {
		t.Fatalf("conversations=%#v err=%v", conversations, err)
	}
	wantedUnread := map[string]int{"external-chat-1": 2, "external-chat-2": 1}
	for _, conversation := range conversations {
		if expected, ok := wantedUnread[conversation.ExternalID]; ok && conversation.UnreadCount != expected {
			t.Fatalf("unread count was not stored for %s: %#v", conversation.ExternalID, conversation)
		}
	}
	messages, err := repository.ConversationMessages(ctx, "conversation-1")
	if err != nil || len(messages) != 1 || messages[0].ExternalID != "message-external-1" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	if len(queue.Tasks()) != 2 {
		t.Fatalf("sync tasks=%#v", queue.Tasks())
	}
	for _, queued := range queue.Tasks() {
		if queued.Type != core.TaskConversationSync || queued.ProfileID != "profile-1" {
			t.Fatalf("queued task=%#v", queued)
		}
	}
	if err := handlers.Discover(ctx, task); err != nil {
		t.Fatalf("repeat discovery: %v", err)
	}
	if len(queue.Tasks()) != 2 {
		t.Fatalf("repeat discovery created duplicate sync tasks: %#v", queue.Tasks())
	}
}

func TestConversationDiscoverSyncsPendingQuestionnaireOutsideTheWindow(t *testing.T) {
	ctx := context.Background()
	handlers, _, repository, queue, transport, clock := newConversationHandlersFixture(t)
	handlers.ConfigureKnownAnswers(knownConversationAnswerRegistry(t), func(profileID core.ProfileID) bool {
		return profileID == "profile-1"
	})
	prompt := conversationQuestionnairePrompt(clock.now)
	if _, _, err := repository.AppendConversationMessage(ctx, prompt, clock.now); err != nil {
		t.Fatalf("append questionnaire prompt: %v", err)
	}
	// The observed window no longer contains the conversation, but its
	// unanswered prompt still has a reviewed answer.
	transport.discovery = adapter.ConversationDiscoveryResult{ObservedAt: clock.now}
	payload, _ := json.Marshal(core.ConversationDiscoverPayload{ProfileID: "profile-1"})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "discover-pending", Type: core.TaskConversationDiscover, IdempotencyKey: "discover-run-pending",
		Source: "test", Platform: "hh", ProfileID: "profile-1", CorrelationID: "correlation-pending", Payload: payload,
	}, clock.now)
	if err != nil {
		t.Fatalf("new discovery task: %v", err)
	}
	if err := handlers.Discover(ctx, task); err != nil {
		t.Fatalf("discover conversations: %v", err)
	}
	tasks := queue.Tasks()
	if len(tasks) != 1 || tasks[0].Type != core.TaskConversationSync {
		t.Fatalf("pending questionnaire was not scheduled for sync: %#v", tasks)
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

func TestConversationDiscoverHandlerSkipsUnchangedConversations(t *testing.T) {
	ctx := context.Background()
	handlers, _, _, queue, transport, clock := newConversationHandlersFixture(t)
	transport.discovery = adapter.ConversationDiscoveryResult{
		ObservedAt: clock.now,
		Conversations: []core.ConversationObservation{
			{ExternalID: "external-chat-1", Status: core.ConversationActive},
			{ExternalID: "external-chat-2", Status: core.ConversationActive, UnreadCount: 1},
		},
	}
	newTask := func(key string) core.Task {
		payload, _ := json.Marshal(core.ConversationDiscoverPayload{ProfileID: "profile-1"})
		task, err := core.NewTask(core.NewTaskParams{
			ID: core.TaskID("discover-" + key), Type: core.TaskConversationDiscover, IdempotencyKey: key,
			Source: "test", Platform: "hh", ProfileID: "profile-1", CorrelationID: core.CorrelationID("correlation-" + key), Payload: payload,
		}, clock.now)
		if err != nil {
			t.Fatalf("new discovery task: %v", err)
		}
		return task
	}
	if err := handlers.Discover(ctx, newTask("discover-run-1")); err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	if len(queue.Tasks()) != 1 {
		t.Fatalf("first discovery tasks=%#v", queue.Tasks())
	}
	if err := handlers.Discover(ctx, newTask("discover-run-2")); err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if len(queue.Tasks()) != 1 {
		t.Fatalf("unchanged discovery created sync tasks: %#v", queue.Tasks())
	}
}

func TestConversationDiscoverHandlerToleratesStaleObservationTime(t *testing.T) {
	ctx := context.Background()
	handlers, _, repository, queue, transport, clock := newConversationHandlersFixture(t)

	// A concurrent sync advances the stored conversation past the catalog
	// snapshot discovery captured before it started.
	advanced := core.ConversationMessage{
		ID: "message-advanced", ConversationID: "conversation-1", ExternalID: "external-advanced",
		Direction: core.MessageIncoming, Kind: core.MessageText, Status: core.MessageObserved,
		Text: "later", OccurredAt: clock.now,
	}
	if _, _, err := repository.AppendConversationMessage(ctx, advanced, clock.now.Add(time.Minute)); err != nil {
		t.Fatalf("advance conversation: %v", err)
	}

	transport.discovery = adapter.ConversationDiscoveryResult{
		ObservedAt: clock.now,
		Conversations: []core.ConversationObservation{
			{
				ExternalID: "external-chat-1", Status: core.ConversationActive, UnreadCount: 5,
				LastMessage: &core.ConversationMessageObservation{
					ExternalID: "external-catalog-message", Direction: core.MessageIncoming,
					Kind: core.MessageText, Text: "из каталога", OccurredAt: clock.now,
				},
			},
		},
	}
	payload, _ := json.Marshal(core.ConversationDiscoverPayload{ProfileID: "profile-1"})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "discover-stale", Type: core.TaskConversationDiscover, IdempotencyKey: "discover-stale",
		Source: "test", Platform: "hh", ProfileID: "profile-1", CorrelationID: "correlation-stale", Payload: payload,
	}, clock.now)
	if err != nil {
		t.Fatalf("new discovery task: %v", err)
	}
	if err := handlers.Discover(ctx, task); err != nil {
		t.Fatalf("discover with stale observation: %v", err)
	}
	conversation, err := repository.Conversation(ctx, "conversation-1")
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	if conversation.UnreadCount != 0 {
		t.Fatalf("stale catalog unread was applied: %#v", conversation)
	}
	if len(queue.Tasks()) != 1 || queue.Tasks()[0].Type != core.TaskConversationSync {
		t.Fatalf("sync tasks=%#v", queue.Tasks())
	}
}
