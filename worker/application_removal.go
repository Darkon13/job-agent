package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

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
	if payload.Reason != core.ApplicationRemovalManual {
		observer, err := handler.observers.Resolve(task.ProfileID)
		if err != nil {
			return err
		}
		result, err := observer.ObserveApplicationStates(ctx, task.ProfileID)
		if err != nil {
			return err
		}
		if err := validateApplicationObservation(result, handler.clock.Now()); err != nil {
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
		request.StaleBefore = handler.clock.Now().Add(-payload.StaleAfter.Value())
	}
	// Local GC only. No platform DELETE and no deletion of conversations.
	_, _, err = handler.removals.RemoveApplication(ctx, application.ID, request, handler.clock.Now())
	return err
}
