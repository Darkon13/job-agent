package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type VacancyTestCapturerRegistry struct {
	mu        sync.RWMutex
	capturers map[core.ProfileID]adapter.VacancyTestCapturer
}

func NewVacancyTestCapturerRegistry() *VacancyTestCapturerRegistry {
	return &VacancyTestCapturerRegistry{capturers: make(map[core.ProfileID]adapter.VacancyTestCapturer)}
}

func (registry *VacancyTestCapturerRegistry) Register(profileID core.ProfileID, capturer adapter.VacancyTestCapturer) error {
	if profileID == "" || capturer == nil {
		return errors.New("vacancy test capturer registration requires profile and capturer")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.capturers[profileID]; exists {
		return fmt.Errorf("vacancy test capturer for profile %s is already registered", profileID)
	}
	registry.capturers[profileID] = capturer
	return nil
}

func (registry *VacancyTestCapturerRegistry) Resolve(profileID core.ProfileID) (adapter.VacancyTestCapturer, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	capturer, exists := registry.capturers[profileID]
	if !exists {
		return nil, &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "test.capture", Message: "no vacancy test capturer for profile"}
	}
	return capturer, nil
}

func (registry *VacancyTestCapturerRegistry) Count() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.capturers)
}

// VacancyTestCaptureHandler observes the questionnaire exposed by a vacancy
// response popup and merges every seen question into the progressive test
// catalog. It is a discovery step: no attempt is started and no answer is sent.
type VacancyTestCaptureHandler struct {
	capturers *VacancyTestCapturerRegistry
	catalog   storage.TestCatalogRepository
	clock     Clock
}

func NewVacancyTestCaptureHandler(capturers *VacancyTestCapturerRegistry, catalog storage.TestCatalogRepository, clock Clock) (*VacancyTestCaptureHandler, error) {
	if capturers == nil || catalog == nil || clock == nil {
		return nil, errors.New("vacancy test capture handler requires capturers, catalog and clock")
	}
	return &VacancyTestCaptureHandler{capturers: capturers, catalog: catalog, clock: clock}, nil
}

func (handler *VacancyTestCaptureHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.TestCapturePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode test capture task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("test capture task profile mismatch")
	}
	capturer, err := handler.capturers.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	questionnaire, err := capturer.CaptureVacancyTest(ctx, payload.ProfileID, core.VacancyKey{
		Platform: payload.Platform, ExternalID: payload.VacancyExternalID,
	})
	if err != nil {
		return err
	}
	now := handler.clock.Now()
	definition, err := handler.progressiveDefinition(ctx, payload, questionnaire.Title, now)
	if err != nil {
		return err
	}
	fingerprints := make([]string, 0, len(questionnaire.Questions))
	for _, question := range questionnaire.Questions {
		fingerprint, _, err := definition.ObserveQuestion(question, now)
		if err != nil {
			return err
		}
		fingerprints = append(fingerprints, fingerprint)
	}
	if _, err := definition.CompleteObservedAttempt(fingerprints, now); err != nil {
		return err
	}
	if _, err := handler.catalog.UpsertTestDefinition(ctx, definition); err != nil {
		return err
	}
	return nil
}

func (handler *VacancyTestCaptureHandler) progressiveDefinition(ctx context.Context, payload core.TestCapturePayload, title string, now time.Time) (core.TestDefinition, error) {
	definitions, err := handler.catalog.ListTestDefinitions(ctx, storage.TestDefinitionFilter{Platform: payload.Platform})
	if err != nil {
		return core.TestDefinition{}, err
	}
	externalID := "vacancy:" + payload.VacancyExternalID
	id := core.TestDefinitionID(string(payload.Platform) + ":" + externalID)
	for _, definition := range definitions {
		if definition.ID == id {
			return definition, nil
		}
	}
	return core.NewProgressiveTestDefinition(payload.Platform, externalID, title, nil, now)
}
