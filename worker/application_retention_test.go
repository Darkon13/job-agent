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

type retentionObserver struct {
	result adapter.ApplicationStateObservationResult
}

func (observer retentionObserver) ObserveApplicationStates(context.Context, core.ProfileID) (adapter.ApplicationStateObservationResult, error) {
	return observer.result, nil
}

func storeSubmittedApplication(t *testing.T, repository *storagememory.Repository, id, vacancyID string, submittedAt time.Time) core.Application {
	t.Helper()
	application, err := core.NewApplication(core.ApplicationID(id), core.ApplicationKey{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: vacancyID},
	}, submittedAt.Add(-time.Minute))
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, _, err := repository.CreateApplication(context.Background(), application); err != nil {
		t.Fatalf("create application: %v", err)
	}
	stored, _ := repository.ApplicationByID(context.Background(), application.ID)
	for _, status := range []core.ApplicationStatus{core.ApplicationPreparing, core.ApplicationReady, core.ApplicationSubmitting, core.ApplicationSubmitted} {
		previous := stored.Status
		if err := stored.Transition(status, stored.UpdatedAt.Add(time.Second)); err != nil {
			t.Fatalf("transition to %s: %v", status, err)
		}
		if err := repository.SaveApplication(context.Background(), stored, previous); err != nil {
			t.Fatalf("save %s: %v", status, err)
		}
	}
	stored.SubmittedAt = &submittedAt
	stored.UpdatedAt = submittedAt
	if err := repository.SaveApplication(context.Background(), stored, core.ApplicationSubmitted); err != nil {
		t.Fatalf("set submission time: %v", err)
	}
	return stored
}

func TestApplicationRetentionUsesFreshPlatformStateAndProtectsInvitations(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	oldPending := storeSubmittedApplication(t, repository, "application-old", "vacancy-old", now.Add(-30*24*time.Hour))
	invited := storeSubmittedApplication(t, repository, "application-invited", "vacancy-invited", now.Add(-60*24*time.Hour))
	storeSubmittedApplication(t, repository, "application-rejected", "vacancy-rejected", now.Add(-time.Hour))
	storeSubmittedApplication(t, repository, "application-recent", "vacancy-recent", now.Add(-24*time.Hour))
	storeSubmittedApplication(t, repository, "application-unknown", "vacancy-unknown", now.Add(-60*24*time.Hour))

	viewed := true
	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", retentionObserver{result: adapter.ApplicationStateObservationResult{
		ObservedAt: now,
		Applications: []adapter.ApplicationStateObservation{
			{ExternalNegotiationID: "n-old", ExternalVacancyID: "vacancy-old", PlatformState: "response", Disposition: core.ApplicationDispositionPending, ViewedByOpponent: &viewed},
			{ExternalNegotiationID: "n-invited", ExternalVacancyID: "vacancy-invited", PlatformState: "invitation", Disposition: core.ApplicationDispositionInvited},
			{ExternalNegotiationID: "n-rejected", ExternalVacancyID: "vacancy-rejected", PlatformState: "discard", Disposition: core.ApplicationDispositionRejected},
			{ExternalNegotiationID: "n-recent", ExternalVacancyID: "vacancy-recent", PlatformState: "response", Disposition: core.ApplicationDispositionPending},
			{ExternalNegotiationID: "n-unknown", ExternalVacancyID: "vacancy-unknown", PlatformState: "future", Disposition: core.ApplicationDispositionUnknown},
		},
	}}); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	clock := &conversationClock{now: now}
	removal, err := workflow.NewApplicationRemovalWorkflow(repository, queue, clock, &conversationIDs{})
	if err != nil {
		t.Fatalf("new removal workflow: %v", err)
	}
	handler, err := NewApplicationRetentionHandler(repository, repository, observers, removal, clock)
	if err != nil {
		t.Fatalf("new retention handler: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationRetentionPayload{ProfileID: "primary", StaleAfter: core.Duration(14 * 24 * time.Hour), RemoveRejected: true})
	task := core.Task{ID: "retention-1", Type: core.TaskApplicationRetention, ProfileID: "primary", Platform: "hh", Payload: payload}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle retention: %v", err)
	}
	if len(queue.Tasks()) != 2 {
		t.Fatalf("remove tasks=%d want=2: %#v", len(queue.Tasks()), queue.Tasks())
	}
	for _, queued := range queue.Tasks() {
		var remove core.ApplicationRemovePayload
		if err := json.Unmarshal(queued.Payload, &remove); err != nil {
			t.Fatalf("decode removal: %v", err)
		}
		if remove.ApplicationID == oldPending.ID && remove.Reason != core.ApplicationRemovalRetentionStale {
			t.Fatalf("old pending reason=%s", remove.Reason)
		}
		if remove.ApplicationID == invited.ID {
			t.Fatal("invitation was scheduled for removal")
		}
	}
	state, err := repository.ApplicationPlatformState(context.Background(), oldPending.ID)
	if err != nil || state.Disposition != core.ApplicationDispositionPending || state.ViewedByOpponent == nil || !*state.ViewedByOpponent {
		t.Fatalf("stored platform state=%#v err=%v", state, err)
	}
	if _, err := repository.ApplicationPlatformState(context.Background(), invited.ID); err != nil {
		t.Fatalf("invitation state was not persisted: %v", err)
	}
}

func TestApplicationRemovalIdempotencyDoesNotDependOnBulkRequest(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	application := storeSubmittedApplication(t, repository, "application-1", "vacancy-1", now.Add(-time.Hour))
	removal, _ := workflow.NewApplicationRemovalWorkflow(repository, queue, &conversationClock{now: now}, &conversationIDs{})
	first, created, err := removal.Enqueue(context.Background(), application.ID, "first-selection")
	if err != nil || !created {
		t.Fatalf("first enqueue: task=%#v created=%v err=%v", first, created, err)
	}
	repeated, created, err := removal.Enqueue(context.Background(), application.ID, "another-selection")
	if err != nil || created || repeated.ID != first.ID || len(queue.Tasks()) != 1 {
		t.Fatalf("repeat enqueue: task=%#v created=%v tasks=%d err=%v", repeated, created, len(queue.Tasks()), err)
	}
	if first.IdempotencyKey == fmt.Sprintf("application.remove:%s", application.ID) {
		t.Fatal("idempotency key must not expose raw application id")
	}
}

func TestRemovalReobservesBeforeDeletingAndRetriesAfterCommit(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	a := storeSubmittedApplication(t, repository, "a", "v", now.Add(-30*24*time.Hour))
	clock := &conversationClock{now: now}
	removal, _ := workflow.NewApplicationRemovalWorkflow(repository, queue, clock, &conversationIDs{})
	gcTask, _, err := removal.EnqueueRemoval(ctx, core.ApplicationRemovePayload{ApplicationID: a.ID, Reason: core.ApplicationRemovalRetentionStale, StaleAfter: core.Duration(14 * 24 * time.Hour)}, "gc-run-1")
	if err != nil {
		t.Fatal(err)
	}
	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", retentionObserver{result: adapter.ApplicationStateObservationResult{ObservedAt: now, Applications: []adapter.ApplicationStateObservation{{ExternalNegotiationID: "n", ExternalVacancyID: "v", PlatformState: "invitation", Disposition: core.ApplicationDispositionInvited}}}}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewApplicationRemovalHandler(repository, repository, repository, repository, testConversationTransports(), observers, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, gcTask); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ApplicationByID(ctx, a.ID); err != nil {
		t.Fatal("removed new invitation", err)
	}
	manual, _, err := removal.Enqueue(ctx, a.ID, "manual-selection")
	if err != nil || manual.ID == gcTask.ID {
		t.Fatalf("manual conflicts with GC: %v", err)
	}
	if err := handler.Handle(ctx, manual); err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, manual); err != nil {
		t.Fatal("retry after commit", err)
	}
	again, created, err := removal.Enqueue(ctx, a.ID, "second-click")
	if err != nil || created || again.ID != manual.ID {
		t.Fatalf("repeat API after deletion: %v %v", created, err)
	}
}

func TestApplicationObservationRejectsAmbiguityAndStaleData(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	a := core.Application{ID: "a", Key: core.ApplicationKey{ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "v"}}}
	result := adapter.ApplicationStateObservationResult{ObservedAt: now, Applications: []adapter.ApplicationStateObservation{
		{ExternalNegotiationID: "n1", ExternalVacancyID: "v", PlatformState: "discard", Disposition: core.ApplicationDispositionRejected},
		{ExternalNegotiationID: "n2", ExternalVacancyID: "v", PlatformState: "invitation", Disposition: core.ApplicationDispositionInvited},
	}}
	if err := validateApplicationObservation(result, now); err != nil {
		t.Fatal(err)
	}
	if _, found := matchApplicationObservation(a, result); found {
		t.Fatal("chose arbitrary resume negotiation")
	}
	a.ExternalNegotiationID = "n2"
	if state, found := matchApplicationObservation(a, result); !found || state.Disposition != core.ApplicationDispositionInvited {
		t.Fatal("did not match exact negotiation")
	}
	if validateApplicationObservation(result, now.Add(6*time.Minute)) == nil || validateApplicationObservation(result, now.Add(-time.Minute)) == nil {
		t.Fatal("accepted stale/future observation")
	}
	result.Applications[1] = result.Applications[0]
	if validateApplicationObservation(result, now) == nil {
		t.Fatal("accepted duplicate negotiation")
	}
}

type withdrawingRetentionObserver struct {
	retentionObserver
	withdrawn []string
}

func (observer *withdrawingRetentionObserver) WithdrawApplication(_ context.Context, _ core.ProfileID, state core.ApplicationPlatformState) (adapter.ApplicationWithdrawalResult, error) {
	observer.withdrawn = append(observer.withdrawn, state.ExternalNegotiationID)
	return adapter.ApplicationWithdrawalResult{}, nil
}

func TestApplicationRetentionHidesOrphanRefusals(t *testing.T) {
	now := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	storeSubmittedApplication(t, repository, "application-active", "vacancy-active", now.Add(-time.Hour))
	observer := &withdrawingRetentionObserver{retentionObserver: retentionObserver{result: adapter.ApplicationStateObservationResult{
		ObservedAt: now,
		Applications: []adapter.ApplicationStateObservation{
			{ExternalNegotiationID: "n-orphan", ExternalVacancyID: "vacancy-orphan", PlatformState: "DISCARD", Disposition: core.ApplicationDispositionRejected},
			{ExternalNegotiationID: "n-active", ExternalVacancyID: "vacancy-active", PlatformState: "DISCARD", Disposition: core.ApplicationDispositionRejected},
			{ExternalNegotiationID: "n-invited", ExternalVacancyID: "vacancy-invited", PlatformState: "INVITATION", Disposition: core.ApplicationDispositionInvited},
		},
	}}}
	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", observer); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	clock := &conversationClock{now: now}
	removal, err := workflow.NewApplicationRemovalWorkflow(repository, queue, clock, &conversationIDs{})
	if err != nil {
		t.Fatalf("new removal workflow: %v", err)
	}
	handler, err := NewApplicationRetentionHandler(repository, repository, observers, removal, clock)
	if err != nil {
		t.Fatalf("new retention handler: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationRetentionPayload{ProfileID: "primary", StaleAfter: core.Duration(14 * 24 * time.Hour), RemoveRejected: true})
	task := core.Task{ID: "retention-orphan", Type: core.TaskApplicationRetention, ProfileID: "primary", Platform: "hh", Payload: payload}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle retention: %v", err)
	}
	if len(observer.withdrawn) != 1 || observer.withdrawn[0] != "n-orphan" {
		t.Fatalf("withdrawn=%#v want [n-orphan]", observer.withdrawn)
	}
}

func TestApplicationRetentionPurgesConversationsOfRemovedApplications(t *testing.T) {
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	active := storeSubmittedApplication(t, repository, "application-active", "vacancy-active", now.Add(-time.Hour))

	orphan, err := core.NewConversation("chat-orphan", "hh", "primary", "external-orphan", now)
	if err != nil {
		t.Fatalf("new orphan conversation: %v", err)
	}
	orphan.ApplicationID = "application-removed-long-ago"
	if _, _, err := repository.CreateConversation(context.Background(), orphan); err != nil {
		t.Fatalf("store orphan conversation: %v", err)
	}
	kept, err := core.NewConversation("chat-kept", "hh", "primary", "external-kept", now)
	if err != nil {
		t.Fatalf("new kept conversation: %v", err)
	}
	kept.ApplicationID = active.ID
	if _, _, err := repository.CreateConversation(context.Background(), kept); err != nil {
		t.Fatalf("store kept conversation: %v", err)
	}

	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", retentionObserver{result: adapter.ApplicationStateObservationResult{
		ObservedAt: now,
		Applications: []adapter.ApplicationStateObservation{
			{ExternalNegotiationID: "n-active", ExternalVacancyID: "vacancy-active", PlatformState: "response", Disposition: core.ApplicationDispositionPending},
		},
	}}); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	clock := &conversationClock{now: now}
	removal, err := workflow.NewApplicationRemovalWorkflow(repository, queue, clock, &conversationIDs{})
	if err != nil {
		t.Fatalf("new removal workflow: %v", err)
	}
	handler, err := NewApplicationRetentionHandler(repository, repository, observers, removal, clock)
	if err != nil {
		t.Fatalf("new retention handler: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationRetentionPayload{ProfileID: "primary", StaleAfter: core.Duration(14 * 24 * time.Hour)})
	task := core.Task{ID: "retention-chats", Type: core.TaskApplicationRetention, ProfileID: "primary", Platform: "hh", Payload: payload}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle retention: %v", err)
	}
	conversations, err := repository.ListConversations(context.Background(), storage.ConversationFilter{ProfileID: "primary"})
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(conversations) != 1 || conversations[0].ID != "chat-kept" {
		t.Fatalf("conversations=%#v want only chat-kept", conversations)
	}
}

func TestApplicationRetentionRemovesStoredRejectionsMissingFromObservation(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	application := storeSubmittedApplication(t, repository, "application-hidden-refusal", "vacancy-hidden", now.Add(-40*24*time.Hour))
	if err := repository.SaveApplicationPlatformState(context.Background(), core.ApplicationPlatformState{
		ApplicationID: application.ID, ExternalNegotiationID: "n-hidden", PlatformState: "discard",
		Disposition: core.ApplicationDispositionRejected, ObservedAt: now.Add(-38 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("store platform state: %v", err)
	}
	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", retentionObserver{result: adapter.ApplicationStateObservationResult{
		ObservedAt: now,
	}}); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	clock := &conversationClock{now: now}
	removal, err := workflow.NewApplicationRemovalWorkflow(repository, queue, clock, &conversationIDs{})
	if err != nil {
		t.Fatalf("new removal workflow: %v", err)
	}
	handler, err := NewApplicationRetentionHandler(repository, repository, observers, removal, clock)
	if err != nil {
		t.Fatalf("new retention handler: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationRetentionPayload{
		ProfileID: "primary", StaleAfter: core.Duration(30 * 24 * time.Hour), RemoveRejected: true,
	})
	task := core.Task{ID: "retention-hidden-refusal", Type: core.TaskApplicationRetention, ProfileID: "primary", Platform: "hh", Payload: payload}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle retention: %v", err)
	}
	if len(queue.Tasks()) != 1 {
		t.Fatalf("remove tasks=%d want=1: %#v", len(queue.Tasks()), queue.Tasks())
	}
	var remove core.ApplicationRemovePayload
	if err := json.Unmarshal(queue.Tasks()[0].Payload, &remove); err != nil {
		t.Fatalf("decode removal: %v", err)
	}
	if remove.ApplicationID != application.ID || remove.Reason != core.ApplicationRemovalRetentionRejected {
		t.Fatalf("removal=%#v", remove)
	}
}

func TestApplicationRetentionRemovesClosedVacancyApplicationsImmediately(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	vacancy := core.Vacancy{Platform: "hh", ExternalID: "vacancy-closed-card", Title: "Go developer", State: core.VacancyStateArchived, ObservedAt: now.Add(-time.Hour)}
	if _, err := repository.UpsertVacancy(ctx, vacancy); err != nil {
		t.Fatalf("store vacancy: %v", err)
	}
	application, err := core.NewApplication("application-closed-card", core.ApplicationKey{ProfileID: "primary", Vacancy: vacancy.Key()}, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, _, err := repository.CreateApplication(ctx, application); err != nil {
		t.Fatalf("create application: %v", err)
	}
	if err := application.Transition(core.ApplicationPreparing, now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("preparing: %v", err)
	}
	if err := repository.SaveApplication(ctx, application, core.ApplicationNew); err != nil {
		t.Fatalf("save preparing: %v", err)
	}
	application.DecisionCode = "vacancy_closed"
	application.DecisionReason = "HH не разрешает отклик: вакансия в архиве или недоступна"
	if err := application.Transition(core.ApplicationSkipped, now.Add(-20*time.Minute)); err != nil {
		t.Fatalf("skip: %v", err)
	}
	if err := repository.SaveApplication(ctx, application, core.ApplicationPreparing); err != nil {
		t.Fatalf("save skipped: %v", err)
	}
	observers := NewApplicationStateObserverRegistry()
	if err := observers.Register("primary", retentionObserver{result: adapter.ApplicationStateObservationResult{ObservedAt: now}}); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	clock := &conversationClock{now: now}
	removal, err := workflow.NewApplicationRemovalWorkflow(repository, queue, clock, &conversationIDs{})
	if err != nil {
		t.Fatalf("new removal workflow: %v", err)
	}
	handler, err := NewApplicationRetentionHandler(repository, repository, observers, removal, clock)
	if err != nil {
		t.Fatalf("new retention handler: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationRetentionPayload{
		ProfileID: "primary", StaleAfter: core.Duration(30 * 24 * time.Hour),
		RemoveWaitingValidation: true, ValidationStaleAfter: core.Duration(7 * 24 * time.Hour),
	})
	task := core.Task{ID: "retention-closed-card", Type: core.TaskApplicationRetention, ProfileID: "primary", Platform: "hh", Payload: payload}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle retention: %v", err)
	}
	if len(queue.Tasks()) != 1 {
		t.Fatalf("remove tasks=%d want=1", len(queue.Tasks()))
	}
}
