package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	states        storage.ApplicationPlatformStateRepository
	conversations storage.ConversationPurgeRepository
	observers     *ApplicationStateObserverRegistry
	removal       *workflow.ApplicationRemovalWorkflow
	clock         Clock
}

func NewApplicationRetentionHandler(states storage.ApplicationPlatformStateRepository, conversations storage.ConversationPurgeRepository, observers *ApplicationStateObserverRegistry, removal *workflow.ApplicationRemovalWorkflow, clock Clock) (*ApplicationRetentionHandler, error) {
	if states == nil || conversations == nil || observers == nil || removal == nil || clock == nil {
		return nil, errors.New("application retention handler requires states, conversations, observers, removal workflow and clock")
	}
	return &ApplicationRetentionHandler{states: states, conversations: conversations, observers: observers, removal: removal, clock: clock}, nil
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
			// The negotiation may already be hidden on the platform, so the
			// observation no longer lists it. A stored refusal that aged past
			// the window still removes the local record; the next run purges
			// the orphaned platform refusal if it reappears.
			if payload.RemoveRejected {
				if stored, stateErr := handler.states.ApplicationPlatformState(ctx, application.ID); stateErr == nil &&
					stored.Disposition == core.ApplicationDispositionRejected {
					if _, _, err := handler.removal.EnqueueRemoval(ctx, core.ApplicationRemovePayload{
						ApplicationID: application.ID, Reason: core.ApplicationRemovalRetentionRejected, StaleAfter: payload.StaleAfter,
					}, "retention:"+string(task.ID)); err != nil {
						return err
					}
				}
			}
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
	// Conversations follow their application: chats of already removed
	// applications would otherwise stay in the dashboard forever.
	purgedConversations, err := handler.conversations.PurgeOrphanConversations(ctx, payload.ProfileID)
	if err != nil {
		return err
	}
	if purgedConversations > 0 {
		slog.Default().Info("removed conversations of deleted applications",
			"profile", payload.ProfileID, "conversations", purgedConversations)
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
		if application.Key.Vacancy.Platform != task.Platform {
			continue
		}
		// A vacancy that closed is a dead end: the record goes immediately
		// instead of waiting for the age window.
		deadEnd := application.Status == core.ApplicationSkipped && application.DecisionCode == "vacancy_closed"
		if application.UpdatedAt.After(validationStaleBefore) && !deadEnd {
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
