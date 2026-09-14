package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// freshRemovalStateWindow bounds how old a stored platform state may be for a
// retention removal to reuse it instead of re-reading the negotiation list.
const freshRemovalStateWindow = 10 * time.Minute

type ApplicationRemovalHandler struct {
	applications storage.ApplicationReadRepository
	removals     storage.ApplicationRemovalRepository
	states       storage.ApplicationPlatformStateRepository
	observers    *ApplicationStateObserverRegistry
	clock        Clock
}

func NewApplicationRemovalHandler(applications storage.ApplicationReadRepository, removals storage.ApplicationRemovalRepository, states storage.ApplicationPlatformStateRepository, observers *ApplicationStateObserverRegistry, clock Clock) (*ApplicationRemovalHandler, error) {
	if applications == nil || removals == nil || states == nil || observers == nil || clock == nil {
		return nil, errors.New("application removal requires applications, removals, states, observers and clock")
	}
	return &ApplicationRemovalHandler{applications, removals, states, observers, clock}, nil
}

func (handler *ApplicationRemovalHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ApplicationRemovePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode application remove payload: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	tombstone, removed, err := handler.removals.ApplicationTombstone(ctx, payload.ApplicationID)
	if err != nil {
		return err
	}
	if removed {
		if tombstone.Key.ProfileID != task.ProfileID || tombstone.Key.Vacancy.Platform != task.Platform {
			return errors.New("removed application identity mismatch")
		}
		return nil // Crash after DB commit and before task completion: already done.
	}
	application, err := handler.applications.ApplicationByID(ctx, payload.ApplicationID)
	if err != nil {
		return err
	}
	if application.Key.ProfileID != task.ProfileID || application.Key.Vacancy.Platform != task.Platform {
		return errors.New("application remove task identity does not match target")
	}
	request := core.ApplicationRemoval{Reason: payload.Reason}
	var observed *core.ApplicationPlatformState
	if payload.Reason != core.ApplicationRemovalManual {
		// Retention saves fresh states right before enqueueing removals. Reuse
		// them instead of re-reading the whole negotiation list per removal.
		now := handler.clock.Now()
		if state, stateErr := handler.states.ApplicationPlatformState(ctx, application.ID); stateErr == nil &&
			!state.ObservedAt.IsZero() && now.Sub(state.ObservedAt) <= freshRemovalStateWindow {
			request.ObservedAt = state.ObservedAt
			request.StaleBefore = now.Add(-payload.StaleAfter.Value())
			observed = &state
		} else {
			observer, err := handler.observers.Resolve(task.ProfileID)
			if err != nil {
				return err
			}
			result, err := observer.ObserveApplicationStates(ctx, task.ProfileID)
			if err != nil {
				return err
			}
			if err := validateApplicationObservation(result, now); err != nil {
				return err
			}
			state, found := matchApplicationObservation(application, result)
			if !found {
				return nil
			} // Missing/ambiguous is not proof of rejection.
			if err := handler.states.SaveApplicationPlatformState(ctx, state); err != nil {
				return err
			}
			request.ObservedAt = state.ObservedAt
			request.StaleBefore = now.Add(-payload.StaleAfter.Value())
			observed = &state
		}
	}
	if err := handler.withdrawOnPlatform(ctx, task, application, observed); err != nil {
		return err
	}
	_, _, err = handler.removals.RemoveApplication(ctx, application.ID, request, handler.clock.Now())
	return err
}

// withdrawOnPlatform cancels a pending response or hides a closed negotiation
// so the removal is not local-only. Profiles without a platform withdrawer
// (for example imported history) keep the local-only semantics.
func (handler *ApplicationRemovalHandler) withdrawOnPlatform(
	ctx context.Context,
	task core.Task,
	application core.Application,
	observed *core.ApplicationPlatformState,
) error {
	if !handler.observers.Has(task.ProfileID) {
		return nil
	}
	observer, err := handler.observers.Resolve(task.ProfileID)
	if err != nil {
		return err
	}
	withdrawer, ok := observer.(adapter.ApplicationWithdrawer)
	if !ok {
		return nil
	}
	state := core.ApplicationPlatformState{}
	if observed != nil {
		state = *observed
	} else if stored, storedErr := handler.states.ApplicationPlatformState(ctx, application.ID); storedErr == nil {
		state = stored
	}
	if state.ExternalNegotiationID == "" {
		state.ExternalNegotiationID = application.ExternalNegotiationID
	}
	if state.ExternalNegotiationID == "" {
		return nil
	}
	_, err = withdrawer.WithdrawApplication(ctx, task.ProfileID, state)
	return err
}
