package worker

import (
	"context"
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
	result      adapter.ApplicationStateObservationResult
	withdrawals []core.ApplicationPlatformState
	err         error
}

func (observer *withdrawingObserver) ObserveApplicationStates(context.Context, core.ProfileID) (adapter.ApplicationStateObservationResult, error) {
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
	handler, err := NewApplicationRemovalHandler(repository, repository, repository, observers, clock)
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
	handler, err := NewApplicationRemovalHandler(repository, repository, repository, observers, clock)
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
