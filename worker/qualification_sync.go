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

type QualificationCatalogRegistry struct {
	mu      sync.RWMutex
	readers map[core.ProfileID]adapter.QualificationCatalogReader
}

func NewQualificationCatalogRegistry() *QualificationCatalogRegistry {
	return &QualificationCatalogRegistry{readers: make(map[core.ProfileID]adapter.QualificationCatalogReader)}
}

func (registry *QualificationCatalogRegistry) Register(profileID core.ProfileID, reader adapter.QualificationCatalogReader) error {
	if profileID == "" || reader == nil {
		return errors.New("qualification catalog registration requires profile and reader")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.readers[profileID]; exists {
		return fmt.Errorf("qualification catalog for profile %s is already registered", profileID)
	}
	registry.readers[profileID] = reader
	return nil
}

func (registry *QualificationCatalogRegistry) Resolve(profileID core.ProfileID) (adapter.QualificationCatalogReader, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	reader, exists := registry.readers[profileID]
	if !exists {
		return nil, &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "skill_verification.sync",
			Message: "profile has no qualification catalog reader",
		}
	}
	return reader, nil
}

func (registry *QualificationCatalogRegistry) Count() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.readers)
}

// QualificationSyncHandler refreshes the observed skill verification catalog.
// It only reads the platform: starting an attempt stays an explicit command.
type QualificationSyncHandler struct {
	readers *QualificationCatalogRegistry
	catalog storage.QualificationCatalogRepository
}

func NewQualificationSyncHandler(readers *QualificationCatalogRegistry, catalog storage.QualificationCatalogRepository) (*QualificationSyncHandler, error) {
	if readers == nil || catalog == nil {
		return nil, errors.New("qualification sync handler requires readers and catalog")
	}
	return &QualificationSyncHandler{readers: readers, catalog: catalog}, nil
}

func (handler *QualificationSyncHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.SkillVerificationSyncPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode skill verification sync task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("skill verification sync task profile mismatch")
	}
	reader, err := handler.readers.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	offerings, err := reader.SyncQualifications(ctx, payload.ProfileID)
	if err != nil {
		return err
	}
	if len(offerings) == 0 {
		return nil
	}
	platform := offerings[0].Platform
	return handler.catalog.UpsertQualificationOfferings(ctx, platform, payload.ProfileID, offerings)
}
