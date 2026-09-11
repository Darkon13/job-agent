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

const maximumQualificationQuestions = 200

type QualificationAttemptRegistry struct {
	mu       sync.RWMutex
	services map[core.ProfileID]adapter.QualificationAttemptService
}

func NewQualificationAttemptRegistry() *QualificationAttemptRegistry {
	return &QualificationAttemptRegistry{services: make(map[core.ProfileID]adapter.QualificationAttemptService)}
}

func (registry *QualificationAttemptRegistry) Register(profileID core.ProfileID, service adapter.QualificationAttemptService) error {
	if profileID == "" || service == nil {
		return errors.New("qualification attempt registration requires profile and service")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.services[profileID]; exists {
		return fmt.Errorf("qualification attempt service for profile %s is already registered", profileID)
	}
	registry.services[profileID] = service
	return nil
}

func (registry *QualificationAttemptRegistry) Resolve(profileID core.ProfileID) (adapter.QualificationAttemptService, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	service, exists := registry.services[profileID]
	if !exists {
		return nil, &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "skill_verification.start",
			Message: "profile has no qualification attempt service",
		}
	}
	return service, nil
}

// QualificationAnswerBlocks provides the reviewed question bank of one
// platform qualification family/level.
type QualificationAnswerBlocks interface {
	FindQualificationLevel(platform core.Platform, familyID, levelID string) (core.AnswerBlock, bool)
}

// QualificationStartHandler runs an explicitly started qualification attempt
// and answers it from a reusable reviewed block. It never submits a guessed
// answer: a missing question finishes the attempt early and reports the
// fingerprint for review.
type QualificationStartHandler struct {
	registry QualificationAttemptRegistryReader
	catalog  storage.QualificationCatalogRepository
	results  storage.QualificationRepository
	tests    storage.TestCatalogRepository
	blocks   QualificationAnswerBlocks
	clock    Clock
}

type QualificationAttemptRegistryReader interface {
	Resolve(profileID core.ProfileID) (adapter.QualificationAttemptService, error)
}

func NewQualificationStartHandler(registry QualificationAttemptRegistryReader, catalog storage.QualificationCatalogRepository, results storage.QualificationRepository, tests storage.TestCatalogRepository, blocks QualificationAnswerBlocks, clock Clock) (*QualificationStartHandler, error) {
	if registry == nil || catalog == nil || results == nil || tests == nil || blocks == nil || clock == nil {
		return nil, errors.New("qualification start handler requires registry, catalog, results, test catalog, blocks and clock")
	}
	return &QualificationStartHandler{registry: registry, catalog: catalog, results: results, tests: tests, blocks: blocks, clock: clock}, nil
}

func (handler *QualificationStartHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.SkillVerificationStartPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode skill verification start task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("skill verification start task profile mismatch")
	}
	offerings, err := handler.catalog.QualificationOfferings(ctx, payload.Platform, payload.ProfileID)
	if err != nil {
		return err
	}
	offering, found := findQualificationOffering(offerings, payload.OfferingID)
	if !found {
		return qualificationStartError("qualification offering is not in the observed catalog; run sync first")
	}
	best, hasBest, err := handler.results.BestQualificationResult(ctx, payload.Platform, payload.ProfileID,
		offering.Qualification.FamilyID, offering.Qualification.LevelID)
	if err != nil {
		return err
	}
	if hasBest && best.Passed() {
		// A passed level is not retried automatically. Reviewed blocks stay
		// reusable for other profiles and for a renewed offering.
		return nil
	}
	block, found := handler.blocks.FindQualificationLevel(payload.Platform, offering.Qualification.FamilyID, offering.Qualification.LevelID)
	if !found {
		return qualificationStartError("no reviewed answer block for this level; run a reviewed attempt first")
	}
	service, err := handler.registry.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	return handler.runAttempt(ctx, service, payload, offering, block)
}

func (handler *QualificationStartHandler) runAttempt(ctx context.Context, service adapter.QualificationAttemptService, payload core.SkillVerificationStartPayload, offering core.QualificationOffering, block core.AnswerBlock) error {
	now := handler.clock.Now()
	session, err := service.StartQualification(ctx, payload.ProfileID, payload.OfferingID)
	if err != nil {
		return err
	}
	definition, err := handler.progressiveDefinition(ctx, offering, now)
	if err != nil {
		return err
	}
	for questionCount := 0; questionCount < maximumQualificationQuestions; questionCount++ {
		question, err := service.CurrentQuestion(ctx, session)
		if err != nil {
			return err
		}
		fingerprint, _, err := definition.ObserveQuestion(question, handler.clock.Now())
		if err != nil {
			return err
		}
		if _, err := handler.tests.UpsertTestDefinition(ctx, definition); err != nil {
			return err
		}
		resolved, err := core.ResolveQuestionAnswer(question, block)
		if err != nil {
			if finishErr := service.FinishQualification(ctx, session); finishErr != nil {
				return finishErr
			}
			return qualificationStartError("question " + fingerprint + " has no reusable answer; attempt finished early for review")
		}
		if err := service.SubmitAnswer(ctx, session, resolved); err != nil {
			return err
		}
		result, found, err := service.AttemptResult(ctx, session)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		attempt := core.QualificationAttempt{
			Platform: payload.Platform, ProfileID: payload.ProfileID,
			Qualification: offering.Qualification, Result: result,
			AttemptFingerprint: definition.LastAttemptFingerprint,
		}
		if _, _, err := handler.results.SaveQualificationAttempt(ctx, attempt, handler.clock.Now()); err != nil {
			return err
		}
		return nil
	}
	if err := service.FinishQualification(ctx, session); err != nil {
		return err
	}
	return qualificationStartError("qualification attempt exceeded the question bound")
}

func (handler *QualificationStartHandler) progressiveDefinition(ctx context.Context, offering core.QualificationOffering, now time.Time) (core.TestDefinition, error) {
	definitions, err := handler.tests.ListTestDefinitions(ctx, storage.TestDefinitionFilter{
		Platform: offering.Platform, FamilyID: offering.Qualification.FamilyID, LevelID: offering.Qualification.LevelID,
	})
	if err != nil {
		return core.TestDefinition{}, err
	}
	qualification := offering.Qualification
	candidate, err := core.NewProgressiveTestDefinition(offering.Platform, offering.ExternalID, offering.ExternalID, &qualification, now)
	if err != nil {
		return core.TestDefinition{}, err
	}
	for _, definition := range definitions {
		if definition.ID == candidate.ID {
			return definition, nil
		}
	}
	return candidate, nil
}

func findQualificationOffering(offerings []core.QualificationOffering, id core.QualificationID) (core.QualificationOffering, bool) {
	for _, offering := range offerings {
		if offering.ID == id {
			return offering, true
		}
	}
	return core.QualificationOffering{}, false
}

func qualificationStartError(message string) error {
	return &core.OperationError{
		Category: core.ErrorValidationRequired, Operation: "skill_verification.start",
		Message: message,
	}
}
