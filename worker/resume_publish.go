package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

type ResumePublisherRegistry struct {
	mu         sync.RWMutex
	publishers map[core.ProfileID]adapter.ResumePublisher
}

func NewResumePublisherRegistry() *ResumePublisherRegistry {
	return &ResumePublisherRegistry{publishers: make(map[core.ProfileID]adapter.ResumePublisher)}
}

func (registry *ResumePublisherRegistry) Register(profileID core.ProfileID, publisher adapter.ResumePublisher) error {
	if profileID == "" || publisher == nil {
		return errors.New("resume publisher registration requires profile and publisher")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.publishers[profileID]; exists {
		return fmt.Errorf("resume publisher for profile %s is already registered", profileID)
	}
	registry.publishers[profileID] = publisher
	return nil
}

func (registry *ResumePublisherRegistry) Resolve(profileID core.ProfileID) (adapter.ResumePublisher, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	publisher, exists := registry.publishers[profileID]
	if !exists {
		return nil, &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "resumes.route", Message: "no resume publisher for profile"}
	}
	return publisher, nil
}

func (registry *ResumePublisherRegistry) Has(profileID core.ProfileID) bool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	_, exists := registry.publishers[profileID]
	return exists
}

func (registry *ResumePublisherRegistry) Count() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.publishers)
}

type ResumePublishHandler struct {
	publishers *ResumePublisherRegistry
}

func NewResumePublishHandler(publishers *ResumePublisherRegistry) (*ResumePublishHandler, error) {
	if publishers == nil {
		return nil, errors.New("resume publish handler requires publisher registry")
	}
	return &ResumePublishHandler{publishers: publishers}, nil
}

func (handler *ResumePublishHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ResumePublishPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode resume publish task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("resume publish task profile mismatch")
	}
	publisher, err := handler.publishers.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	_, err = publisher.PublishResume(ctx, adapter.ResumePublishCommand{
		ProfileID: payload.ProfileID, ResumeID: payload.ResumeID, IdempotencyKey: task.IdempotencyKey,
	})
	return err
}
