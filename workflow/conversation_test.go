package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type mutableClock struct{ now time.Time }

func (clock *mutableClock) Now() time.Time { return clock.now }

func newConversationWorkflowFixture(t *testing.T) (*ConversationWorkflow, *storagememory.Repository, *brokermemory.Queue, *mutableClock) {
	t.Helper()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	conversation, err := core.NewConversation("conversation-1", "hh", "profile-1", "external-chat-1", now)
	if err != nil {
		t.Fatalf("new conversation: %v", err)
	}
	if _, _, err := repository.CreateConversation(context.Background(), conversation); err != nil {
		t.Fatalf("store conversation: %v", err)
	}
	queue := brokermemory.NewQueue()
	clock := &mutableClock{now: now}
	service, err := NewConversationWorkflow(repository, queue, clock, &sequentialIDs{})
	if err != nil {
		t.Fatalf("new conversation workflow: %v", err)
	}
	return service, repository, queue, clock
}

func TestFollowUpScheduleReconcileAndSendLifecycle(t *testing.T) {
	ctx := context.Background()
	service, repository, queue, clock := newConversationWorkflowFixture(t)
	runAt := clock.now.Add(72 * time.Hour)
	request := ScheduleFollowUpRequest{
		ConversationID: "conversation-1", AnchorMessageID: "message-1", AnchorAt: clock.now,
		RunAt: runAt, Content: core.MessageContent{TemplateTag: "remind-employer"},
		Policy:         core.FollowUpPolicy{CancelOnIncoming: true, RequireActiveConversation: true, MaxFollowUps: 1},
		IdempotencyKey: "api-request-1",
	}
	followUp, created, err := service.ScheduleFollowUp(ctx, request)
	if err != nil || !created {
		t.Fatalf("schedule follow-up: created=%t err=%v", created, err)
	}
	again, created, err := service.ScheduleFollowUp(ctx, request)
	if err != nil || created || again.ID != followUp.ID {
		t.Fatalf("repeat schedule: stored=%#v created=%t err=%v", again, created, err)
	}
	result, err := service.ReconcileDueFollowUps(ctx, clock.now)
	if err != nil || result.Due != 0 || len(queue.Tasks()) != 0 {
		t.Fatalf("early reconcile: result=%#v tasks=%d err=%v", result, len(queue.Tasks()), err)
	}
	clock.now = runAt
	result, err = service.ReconcileDueFollowUps(ctx, clock.now)
	if err != nil || result.Due != 1 || result.TasksCreated != 1 || len(queue.Tasks()) != 1 {
		t.Fatalf("due reconcile: result=%#v tasks=%d err=%v", result, len(queue.Tasks()), err)
	}
	result, err = service.ReconcileDueFollowUps(ctx, clock.now)
	if err != nil || result.TasksCreated != 0 || len(queue.Tasks()) != 1 {
		t.Fatalf("idempotent reconcile: result=%#v tasks=%d err=%v", result, len(queue.Tasks()), err)
	}
	prepared, outcome, err := service.PrepareFollowUp(ctx, followUp.ID)
	if err != nil || outcome != FollowUpReady || prepared.Status != core.FollowUpQueued {
		t.Fatalf("prepare follow-up: follow-up=%#v outcome=%s err=%v", prepared, outcome, err)
	}
	clock.now = clock.now.Add(time.Second)
	sent, err := service.MarkFollowUpSent(ctx, followUp.ID, "sent-message-1")
	if err != nil || sent.Status != core.FollowUpSent {
		t.Fatalf("mark follow-up sent: follow-up=%#v err=%v", sent, err)
	}
	stored, err := repository.FollowUp(ctx, followUp.ID)
	if err != nil || stored.SentMessageID != "sent-message-1" {
		t.Fatalf("stored sent follow-up: %#v err=%v", stored, err)
	}
}

func TestFollowUpPreparationCancelsAfterIncomingMessage(t *testing.T) {
	ctx := context.Background()
	service, repository, _, clock := newConversationWorkflowFixture(t)
	runAt := clock.now.Add(72 * time.Hour)
	followUp, _, err := service.ScheduleFollowUp(ctx, ScheduleFollowUpRequest{
		ConversationID: "conversation-1", AnchorMessageID: "outgoing-1", AnchorAt: clock.now,
		RunAt: runAt, Content: core.MessageContent{OperatorTag: "reminder-writer"},
		Policy:         core.FollowUpPolicy{CancelOnIncoming: true, RequireActiveConversation: true, MaxFollowUps: 1},
		IdempotencyKey: "api-request-cancel",
	})
	if err != nil {
		t.Fatalf("schedule follow-up: %v", err)
	}
	incomingAt := clock.now.Add(time.Hour)
	_, _, err = repository.AppendConversationMessage(ctx, core.ConversationMessage{
		ID: "incoming-1", ConversationID: "conversation-1", Direction: core.MessageIncoming,
		Kind: core.MessageText, Status: core.MessageObserved, Text: "Спасибо, давайте обсудим",
		OccurredAt: incomingAt,
	}, incomingAt)
	if err != nil {
		t.Fatalf("append incoming message: %v", err)
	}
	clock.now = runAt
	prepared, outcome, err := service.PrepareFollowUp(ctx, followUp.ID)
	if err != nil || outcome != FollowUpCancelled || prepared.CancelReason != core.FollowUpIncomingReceived {
		t.Fatalf("prepare cancelled follow-up: follow-up=%#v outcome=%s err=%v", prepared, outcome, err)
	}
}

func TestConversationCommandsAreIdempotentPerOperation(t *testing.T) {
	ctx := context.Background()
	service, _, queue, _ := newConversationWorkflowFixture(t)
	task, created, err := service.EnqueueMessage(ctx, "conversation-1", core.MessageContent{Text: "Здравствуйте"}, "", "request-1")
	if err != nil || !created || task.Type != core.TaskConversationSend {
		t.Fatalf("enqueue message: task=%#v created=%t err=%v", task, created, err)
	}
	_, created, err = service.EnqueueMessage(ctx, "conversation-1", core.MessageContent{Text: "Здравствуйте"}, "", "request-1")
	if err != nil || created {
		t.Fatalf("repeat message: created=%t err=%v", created, err)
	}
	markRead, created, err := service.EnqueueMarkRead(ctx, "conversation-1", "request-1")
	if err != nil || !created || markRead.Type != core.TaskConversationMarkRead || len(queue.Tasks()) != 2 {
		t.Fatalf("enqueue mark-read: task=%#v created=%t tasks=%d err=%v", markRead, created, len(queue.Tasks()), err)
	}
	var payload core.ConversationSendPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.Content.Text != "Здравствуйте" {
		t.Fatalf("decode send payload: %#v err=%v", payload, err)
	}
}

func TestConversationWorkflowStoresPresentationFromTransport(t *testing.T) {
	ctx := context.Background()
	service, repository, _, clock := newConversationWorkflowFixture(t)
	presentation := core.ConversationPresentation{VacancyTitle: "Go developer", Employer: "Example", VacancyURL: "https://hh.ru/vacancy/42"}
	if err := service.ObserveConversationPresentation(ctx, "conversation-1", presentation, clock.now.Add(time.Minute)); err != nil {
		t.Fatalf("observe presentation: %v", err)
	}
	stored, err := repository.Conversation(ctx, "conversation-1")
	if err != nil || stored.VacancyTitle != presentation.VacancyTitle || stored.Employer != presentation.Employer || stored.VacancyURL != presentation.VacancyURL || stored.Revision != 2 {
		t.Fatalf("stored conversation: %#v err=%v", stored, err)
	}
	if err := service.ObserveConversationPresentation(ctx, "conversation-1", presentation, clock.now.Add(2*time.Minute)); err != nil {
		t.Fatalf("repeat presentation: %v", err)
	}
	again, err := repository.Conversation(ctx, "conversation-1")
	if err != nil || again.Revision != stored.Revision {
		t.Fatalf("repeat changed conversation: %#v err=%v", again, err)
	}
}

type conflictOnceRepository struct {
	*storagememory.Repository
	conflicts int
}

func (repository *conflictOnceRepository) SaveConversation(ctx context.Context, candidate core.Conversation, expectedRevision uint64) error {
	if repository.conflicts > 0 {
		repository.conflicts--
		current, err := repository.Repository.Conversation(ctx, candidate.ID)
		if err != nil {
			return err
		}
		next := current
		next.Revision = current.Revision + 1
		next.UpdatedAt = current.UpdatedAt.Add(time.Second)
		if err := repository.Repository.SaveConversation(ctx, next, current.Revision); err != nil {
			return err
		}
		return storage.ErrRevisionConflict
	}
	return repository.Repository.SaveConversation(ctx, candidate, expectedRevision)
}

func TestConversationPresentationRetriesRevisionConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	base := storagememory.NewRepository()
	conversation, err := core.NewConversation("conversation-1", "hh", "profile-1", "external-chat-1", now)
	if err != nil {
		t.Fatalf("new conversation: %v", err)
	}
	if _, _, err := base.CreateConversation(ctx, conversation); err != nil {
		t.Fatalf("store conversation: %v", err)
	}
	repository := &conflictOnceRepository{Repository: base, conflicts: 1}
	workflow, err := NewConversationWorkflow(repository, brokermemory.NewQueue(), &mutableClock{now: now}, &sequentialIDs{})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	if err := workflow.ObserveConversationPresentation(ctx, conversation.ID, core.ConversationPresentation{
		VacancyTitle: "Go developer", Employer: "Example",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("observe presentation: %v", err)
	}
	if repository.conflicts != 0 {
		t.Fatalf("conflict was not consumed: %d", repository.conflicts)
	}
	stored, err := base.Conversation(ctx, conversation.ID)
	if err != nil || stored.VacancyTitle != "Go developer" || stored.Revision != 3 {
		t.Fatalf("stored conversation = %#v err=%v", stored, err)
	}
}
