package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type ProfileActivityObserverRegistry struct {
	mu        sync.RWMutex
	observers map[core.ProfileID]adapter.ProfileActivityObserver
}

func NewProfileActivityObserverRegistry() *ProfileActivityObserverRegistry {
	return &ProfileActivityObserverRegistry{observers: make(map[core.ProfileID]adapter.ProfileActivityObserver)}
}

func (registry *ProfileActivityObserverRegistry) Register(profileID core.ProfileID, observer adapter.ProfileActivityObserver) error {
	if profileID == "" || observer == nil {
		return errors.New("profile activity observer registration requires profile and observer")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.observers[profileID]; exists {
		return fmt.Errorf("profile activity observer for profile %s is already registered", profileID)
	}
	registry.observers[profileID] = observer
	return nil
}

func (registry *ProfileActivityObserverRegistry) Resolve(profileID core.ProfileID) (adapter.ProfileActivityObserver, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	observer, exists := registry.observers[profileID]
	if !exists {
		return nil, &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "activity.route", Message: "no profile activity observer for profile"}
	}
	return observer, nil
}

func (registry *ProfileActivityObserverRegistry) Has(profileID core.ProfileID) bool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	_, exists := registry.observers[profileID]
	return exists
}

func (registry *ProfileActivityObserverRegistry) Count() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.observers)
}

type ProfileActivityObserveHandler struct {
	repository storage.ProfileActivitySnapshotRepository
	observers  *ProfileActivityObserverRegistry
}

func NewProfileActivityObserveHandler(repository storage.ProfileActivitySnapshotRepository, observers *ProfileActivityObserverRegistry) (*ProfileActivityObserveHandler, error) {
	if repository == nil || observers == nil {
		return nil, errors.New("profile activity observation handler requires repository and observer registry")
	}
	return &ProfileActivityObserveHandler{repository: repository, observers: observers}, nil
}

func (handler *ProfileActivityObserveHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ProfileActivityObservePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode profile activity observation task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if task.Type != core.TaskProfileActivityObserve || task.ProfileID != payload.ProfileID {
		return errors.New("profile activity observation task identity mismatch")
	}
	observer, err := handler.observers.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	observation, err := observer.ObserveProfileActivity(ctx, payload.ProfileID, payload.ResumeID)
	if err != nil {
		return err
	}
	snapshot, err := core.NewProfileActivitySnapshot(task.Platform, payload.ProfileID, payload.ResumeID, task.IdempotencyKey, observation.ObservedAt)
	if err != nil {
		return err
	}
	snapshot.Score = observation.Score
	snapshot.ScoreHidden = observation.ScoreHidden
	snapshot.PeriodDays = observation.PeriodDays
	snapshot.SearchShows = observation.SearchShows
	snapshot.Views = observation.Views
	snapshot.NewViews = observation.NewViews
	snapshot.Invitations = observation.Invitations
	snapshot.NewInvitations = observation.NewInvitations
	snapshot.ResponseStreak = observation.ResponseStreak
	snapshot.ResponsesRequired = observation.ResponsesRequired
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("invalid profile activity observation: %w", err)
	}
	_, err = handler.repository.RecordProfileActivitySnapshot(ctx, snapshot)
	return err
}
