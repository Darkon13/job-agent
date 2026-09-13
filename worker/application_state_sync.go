package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ApplicationStateSyncHandler refreshes the stored platform states of the
// profile's submitted applications. It is read-only towards the platform and
// never removes or modifies applications: retention stays a separate policy.
type ApplicationStateSyncHandler struct {
	states    storage.ApplicationPlatformStateRepository
	observers *ApplicationStateObserverRegistry
	clock     Clock
}

func NewApplicationStateSyncHandler(states storage.ApplicationPlatformStateRepository, observers *ApplicationStateObserverRegistry, clock Clock) (*ApplicationStateSyncHandler, error) {
	if states == nil || observers == nil || clock == nil {
		return nil, errors.New("application state sync handler requires states, observers and clock")
	}
	return &ApplicationStateSyncHandler{states: states, observers: observers, clock: clock}, nil
}

func (handler *ApplicationStateSyncHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ApplicationStateSyncPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode application state sync payload: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("application state sync task profile does not match payload")
	}
	observer, err := handler.observers.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	observed, err := observer.ObserveApplicationStates(ctx, payload.ProfileID)
	if err != nil {
		return err
	}
	if err := validateApplicationObservation(observed, handler.clock.Now()); err != nil {
		return err
	}
	applications, err := handler.states.ListApplicationsForRetention(ctx, payload.ProfileID)
	if err != nil {
		return err
	}
	for _, application := range applications {
		if application.Key.Vacancy.Platform != task.Platform {
			continue
		}
		state, exists := matchApplicationObservation(application, observed)
		if !exists {
			continue
		}
		if err := handler.states.SaveApplicationPlatformState(ctx, state); err != nil {
			return err
		}
	}
	return nil
}
