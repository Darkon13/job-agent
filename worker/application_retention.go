package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

type ApplicationStateObserverRegistry struct {
	mu        sync.RWMutex
	observers map[core.ProfileID]adapter.ApplicationStateObserver
}

func NewApplicationStateObserverRegistry() *ApplicationStateObserverRegistry {
	return &ApplicationStateObserverRegistry{observers: make(map[core.ProfileID]adapter.ApplicationStateObserver)}
}

func (registry *ApplicationStateObserverRegistry) Register(profileID core.ProfileID, observer adapter.ApplicationStateObserver) error {
	if profileID == "" || observer == nil {
		return errors.New("application state observer requires profile and implementation")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.observers[profileID]; exists {
		return fmt.Errorf("application state observer for profile %s is already registered", profileID)
	}
	registry.observers[profileID] = observer
	return nil
}

func (registry *ApplicationStateObserverRegistry) Resolve(profileID core.ProfileID) (adapter.ApplicationStateObserver, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	observer := registry.observers[profileID]
	if observer == nil {
		return nil, &core.OperationError{Category: core.ErrorUnsupported, Operation: "applications.retention", Message: "profile has no application state observer"}
	}
	return observer, nil
}

func (registry *ApplicationStateObserverRegistry) Has(profileID core.ProfileID) bool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return registry.observers[profileID] != nil
}

type ApplicationRetentionHandler struct {
	states    storage.ApplicationPlatformStateRepository
	observers *ApplicationStateObserverRegistry
	removal   *workflow.ApplicationRemovalWorkflow
	clock     Clock
}

func NewApplicationRetentionHandler(states storage.ApplicationPlatformStateRepository, observers *ApplicationStateObserverRegistry, removal *workflow.ApplicationRemovalWorkflow, clock Clock) (*ApplicationRetentionHandler, error) {
	if states == nil || observers == nil || removal == nil || clock == nil {
		return nil, errors.New("application retention handler requires states, observers, removal workflow and clock")
	}
	return &ApplicationRetentionHandler{states: states, observers: observers, removal: removal, clock: clock}, nil
}

func (handler *ApplicationRetentionHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ApplicationRetentionPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode application retention payload: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("application retention task profile does not match payload")
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
	now := handler.clock.Now()
	staleBefore := now.Add(-payload.StaleAfter.Value())
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
		reason := core.ApplicationRemovalReason("")
		switch state.Disposition {
		case core.ApplicationDispositionRejected:
			if payload.RemoveRejected {
				reason = core.ApplicationRemovalRetentionRejected
			}
		case core.ApplicationDispositionPending:
			if application.SubmittedAt != nil && !application.SubmittedAt.After(staleBefore) {
				reason = core.ApplicationRemovalRetentionStale
			}
		case core.ApplicationDispositionInvited, core.ApplicationDispositionUnknown:
			// Invitations and unknown future platform states require an explicit
			// operator selection. Retention must fail safe.
		}
		if reason == "" {
			continue
		}
		if _, _, err := handler.removal.EnqueueRemoval(ctx, core.ApplicationRemovePayload{ApplicationID: application.ID, Reason: reason, StaleAfter: payload.StaleAfter}, "retention:"+string(task.ID)); err != nil {
			return err
		}
	}
	if _, err := handler.purgeOrphanRejections(ctx, task, applications, observed); err != nil {
		return err
	}
	if !payload.RemoveWaitingValidation {
		return nil
	}
	validationStaleBefore := staleBefore
	if payload.ValidationStaleAfter.Value() > 0 {
		validationStaleBefore = now.Add(-payload.ValidationStaleAfter.Value())
	}
	stale, err := handler.states.ListStaleValidationApplications(ctx, payload.ProfileID)
	if err != nil {
		return err
	}
	for _, application := range stale {
		if application.Key.Vacancy.Platform != task.Platform || application.UpdatedAt.After(validationStaleBefore) {
			continue
		}
		removalWindow := payload.StaleAfter
		if payload.ValidationStaleAfter.Value() > 0 {
			removalWindow = payload.ValidationStaleAfter
		}
		if _, _, err := handler.removal.EnqueueRemoval(ctx, core.ApplicationRemovePayload{
			ApplicationID: application.ID, Reason: core.ApplicationRemovalRetentionValidation, StaleAfter: removalWindow,
		}, "retention:"+string(task.ID)); err != nil {
			return err
		}
	}
	return nil
}

// orphanRejectionPurgeLimit bounds how many platform refusals one retention
// run hides so the cleanup never bursts the platform API.
const orphanRejectionPurgeLimit = 50

// purgeOrphanRejections hides platform refusals whose local application left
// the working set. Removed applications keep their refusals visible on the
// platform otherwise, so the platform counter never matches the local set.
func (handler *ApplicationRetentionHandler) purgeOrphanRejections(ctx context.Context, task core.Task, applications []core.Application, observed adapter.ApplicationStateObservationResult) (int, error) {
	observer, err := handler.observers.Resolve(task.ProfileID)
	if err != nil {
		return 0, err
	}
	withdrawer, ok := observer.(adapter.ApplicationWithdrawer)
	if !ok {
		return 0, nil
	}
	activeNegotiations := make(map[string]struct{}, len(applications))
	activeVacancies := make(map[string]struct{}, len(applications))
	for _, application := range applications {
		if application.Key.Vacancy.Platform != task.Platform {
			continue
		}
		if negotiationID := strings.TrimSpace(application.ExternalNegotiationID); negotiationID != "" {
			activeNegotiations[negotiationID] = struct{}{}
		}
		activeVacancies[application.Key.Vacancy.ExternalID] = struct{}{}
	}
	purged := 0
	for _, item := range observed.Applications {
		if item.Disposition != core.ApplicationDispositionRejected {
			continue
		}
		if _, exists := activeNegotiations[item.ExternalNegotiationID]; exists {
			continue
		}
		if _, exists := activeVacancies[item.ExternalVacancyID]; exists {
			continue
		}
		if purged >= orphanRejectionPurgeLimit {
			break
		}
		state := core.ApplicationPlatformState{
			ExternalNegotiationID: item.ExternalNegotiationID,
			PlatformState:         item.PlatformState,
			Disposition:           item.Disposition,
			ObservedAt:            observed.ObservedAt,
		}
		if _, err := withdrawer.WithdrawApplication(ctx, task.ProfileID, state); err != nil {
			return purged, fmt.Errorf("hide orphan refusal %s: %w", item.ExternalNegotiationID, err)
		}
		purged++
	}
	return purged, nil
}
