package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
	"github.com/Darkon13/job-agent/workflow"
)

type withdrawingObserver struct {
	result       adapter.ApplicationStateObservationResult
	withdrawals  []core.ApplicationPlatformState
	observations int
	err          error
}

func (observer *withdrawingObserver) ObserveApplicationStates(context.Context, core.ProfileID) (adapter.ApplicationStateObservationResult, error) {
	observer.observations++
	return observer.result, nil
}

func (observer *withdrawingObserver) WithdrawApplication(_ context.Context, _ core.ProfileID, state core.ApplicationPlatformState) (adapter.ApplicationWithdrawalResult, error) {
	observer.withdrawals = append(observer.withdrawals, state)
	if observer.err != nil {
		return adapter.ApplicationWithdrawalResult{}, observer.err
	}
	return adapter.ApplicationWithdrawalResult{Action: "trash"}, nil
}

func TestApplicationRemovalWithdrawsOnPlatformBeforeLocalRemoval(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	application := storeSubmittedApplication(t, repository, "application-1", "vacancy-1", now.Add(-time.Hour))
	observer := &withdrawingObserver{result: adapter.ApplicationStateObservationResult{
		ObservedAt: now,
		Applications: []adapter.ApplicationStateObservation{{
			ExternalNegotiationID: "n-1", ExternalVacancyID: "vacancy-1",
			PlatformState: "discard", Disposition: core.ApplicationDispositionRejected,
		}},
	}}
	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", observer); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	clock := &conversationClock{now: now}
	removal, err := workflow.NewApplicationRemovalWorkflow(repository, queue, clock, &conversationIDs{})
	if err != nil {
		t.Fatalf("new removal workflow: %v", err)
	}
	handler, err := NewApplicationRemovalHandler(repository, repository, repository, repository, testConversationTransports(), observers, clock)
	if err != nil {
		t.Fatalf("new removal handler: %v", err)
	}
	task, _, err := removal.EnqueueRemoval(ctx, core.ApplicationRemovePayload{
		ApplicationID: application.ID, Reason: core.ApplicationRemovalRetentionRejected, StaleAfter: core.Duration(24 * time.Hour),
	}, "gc-run-1")
	if err != nil {
		t.Fatalf("enqueue removal: %v", err)
	}
	if err := handler.Handle(ctx, task); err != nil {
		t.Fatalf("handle removal: %v", err)
	}
	if len(observer.withdrawals) != 1 || observer.withdrawals[0].ExternalNegotiationID != "n-1" {
		t.Fatalf("withdrawals = %#v", observer.withdrawals)
	}
	if _, err := repository.ApplicationByID(ctx, application.ID); err == nil {
		t.Fatal("application must be removed locally after withdrawal")
	}
}

// steppingClock returns preset moments in order so a test can model work that
// advances the wall clock between the task start and the observation result.
type steppingClock struct {
	times []time.Time
	index int
}

func (clock *steppingClock) Now() time.Time {
	moment := clock.times[clock.index]
	if clock.index < len(clock.times)-1 {
		clock.index++
	}
	return moment
}

func TestApplicationRemovalAcceptsObservationReadAfterTaskStart(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	application := storeSubmittedApplication(t, repository, "application-3", "vacancy-3", start.Add(-time.Hour))
	observer := &withdrawingObserver{result: adapter.ApplicationStateObservationResult{
		ObservedAt: start.Add(2 * time.Second),
		Applications: []adapter.ApplicationStateObservation{{
			ExternalNegotiationID: "n-3", ExternalVacancyID: "vacancy-3",
			PlatformState: "discard", Disposition: core.ApplicationDispositionRejected,
		}},
	}}
	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", observer); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	clock := &steppingClock{times: []time.Time{start, start.Add(2 * time.Second), start.Add(3 * time.Second)}}
	removal, err := workflow.NewApplicationRemovalWorkflow(repository, queue, &conversationClock{now: start}, &conversationIDs{})
	if err != nil {
		t.Fatalf("new removal workflow: %v", err)
	}
	handler, err := NewApplicationRemovalHandler(repository, repository, repository, repository, testConversationTransports(), observers, clock)
	if err != nil {
		t.Fatalf("new removal handler: %v", err)
	}
	task, _, err := removal.EnqueueRemoval(ctx, core.ApplicationRemovePayload{
		ApplicationID: application.ID, Reason: core.ApplicationRemovalRetentionRejected, StaleAfter: core.Duration(24 * time.Hour),
	}, "gc-run-3")
	if err != nil {
		t.Fatalf("enqueue removal: %v", err)
	}
	if err := handler.Handle(ctx, task); err != nil {
		t.Fatalf("handle removal: %v", err)
	}
	if observer.observations != 1 {
		t.Fatalf("live observations = %d, want 1", observer.observations)
	}
	if _, err := repository.ApplicationByID(ctx, application.ID); err == nil {
		t.Fatal("application must be removed after a live observation")
	}
}

func TestApplicationRemovalKeepsLocalRecordWhenWithdrawalFails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	application := storeSubmittedApplication(t, repository, "application-2", "vacancy-2", now.Add(-time.Hour))
	observer := &withdrawingObserver{
		result: adapter.ApplicationStateObservationResult{
			ObservedAt: now,
			Applications: []adapter.ApplicationStateObservation{{
				ExternalNegotiationID: "n-2", ExternalVacancyID: "vacancy-2",
				PlatformState: "discard", Disposition: core.ApplicationDispositionRejected,
			}},
		},
		err: errors.New("platform refused"),
	}
	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", observer); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	clock := &conversationClock{now: now}
	removal, err := workflow.NewApplicationRemovalWorkflow(repository, queue, clock, &conversationIDs{})
	if err != nil {
		t.Fatalf("new removal workflow: %v", err)
	}
	handler, err := NewApplicationRemovalHandler(repository, repository, repository, repository, testConversationTransports(), observers, clock)
	if err != nil {
		t.Fatalf("new removal handler: %v", err)
	}
	task, _, err := removal.EnqueueRemoval(ctx, core.ApplicationRemovePayload{
		ApplicationID: application.ID, Reason: core.ApplicationRemovalRetentionRejected, StaleAfter: core.Duration(24 * time.Hour),
	}, "gc-run-2")
	if err != nil {
		t.Fatalf("enqueue removal: %v", err)
	}
	if err := handler.Handle(ctx, task); err == nil {
		t.Fatal("expected a platform withdrawal failure")
	}
	if _, err := repository.ApplicationByID(ctx, application.ID); err != nil {
		t.Fatal("application must stay local when withdrawal failed")
	}
}

// testConversationTransports returns an empty registry; tests that need a
// platform chat transport register their own fake.
func testConversationTransports() *ConversationTransportRegistry {
	return NewConversationTransportRegistry()
}

type recordingConversationTransport struct {
	readIDs []string
}

func (*recordingConversationTransport) SendConversationMessage(context.Context, adapter.ConversationSendCommand) (core.ConversationMessage, error) {
	return core.ConversationMessage{}, errors.New("unexpected send")
}

func (transport *recordingConversationTransport) MarkConversationRead(_ context.Context, _ core.ProfileID, externalConversationID string) error {
	transport.readIDs = append(transport.readIDs, externalConversationID)
	return nil
}

func (*recordingConversationTransport) SyncConversation(context.Context, core.ProfileID, core.ConversationID, string) (adapter.ConversationSyncResult, error) {
	return adapter.ConversationSyncResult{}, errors.New("unexpected sync")
}

func TestApplicationRemovalMarksLinkedChatsRead(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 15, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	application := storeSubmittedApplication(t, repository, "application-chat", "vacancy-chat", now.Add(-time.Hour))
	conversation, err := core.NewConversation("chat-removal", "hh", "primary", "external-chat-77", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	conversation.ApplicationID = application.ID
	conversation.UnreadCount = 2
	if _, _, err := repository.CreateConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}

	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", retentionObserver{result: adapter.ApplicationStateObservationResult{ObservedAt: now}}); err != nil {
		t.Fatal(err)
	}
	transports := NewConversationTransportRegistry()
	fake := &recordingConversationTransport{}
	if err := transports.Register("primary", fake); err != nil {
		t.Fatal(err)
	}
	handler, err := NewApplicationRemovalHandler(repository, repository, repository, repository, transports, observers, &conversationClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(core.ApplicationRemovePayload{ApplicationID: application.ID, Reason: core.ApplicationRemovalManual})
	task := core.Task{ID: "remove-chat", Type: core.TaskApplicationRemove, ProfileID: "primary", Platform: "hh", Payload: payload}
	if err := handler.Handle(ctx, task); err != nil {
		t.Fatalf("handle removal: %v", err)
	}
	if len(fake.readIDs) != 1 || fake.readIDs[0] != "external-chat-77" {
		t.Fatalf("read=%#v", fake.readIDs)
	}
	conversations, err := repository.ApplicationConversations(ctx, application.ID)
	if err != nil || len(conversations) != 0 {
		t.Fatalf("chats survived: %#v err=%v", conversations, err)
	}
}
