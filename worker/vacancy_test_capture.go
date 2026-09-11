package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// VacancyAnswerBlockResolver returns the reviewed question bank of vacancy
// popup tests for one platform, merging config and appended human revisions.
type VacancyAnswerBlockResolver interface {
	FindVacancy(ctx context.Context, platform core.Platform) (core.AnswerBlock, bool, error)
}

// VacancyTestCaptureHandler observes the questionnaire exposed by a vacancy
// response popup and merges every seen question into the progressive test
// catalog. It is a discovery step: no attempt is started and no answer is sent
// until a reviewed block fully covers the questionnaire.
type VacancyTestCaptureHandler struct {
	capturers *VacancyTestCapturerRegistry
	catalog   storage.TestCatalogRepository
	clock     Clock
	resolver  VacancyAnswerBlockResolver
	reviews   storage.ReviewRepository
	chain     VacancyTestEnqueuer
}

func NewVacancyTestCaptureHandler(capturers *VacancyTestCapturerRegistry, catalog storage.TestCatalogRepository, clock Clock) (*VacancyTestCaptureHandler, error) {
	if capturers == nil || catalog == nil || clock == nil {
		return nil, errors.New("vacancy test capture handler requires capturers, catalog and clock")
	}
	return &VacancyTestCaptureHandler{capturers: capturers, catalog: catalog, clock: clock}, nil
}

// ConfigureAnswerRouting attaches the known-answer step of the chain. With a
// fully covering block the capture enqueues questionnaire.answer; otherwise a
// review session waits for a human without submitting anything.
func (handler *VacancyTestCaptureHandler) ConfigureAnswerRouting(resolver VacancyAnswerBlockResolver, reviews storage.ReviewRepository, chain VacancyTestEnqueuer) {
	if handler == nil {
		return
	}
	handler.resolver = resolver
	handler.reviews = reviews
	handler.chain = chain
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
	return handler.routeAnswers(ctx, payload, definition, questionnaire, now, task)
}

func (handler *VacancyTestCaptureHandler) routeAnswers(ctx context.Context, payload core.TestCapturePayload, definition core.TestDefinition, questionnaire core.Questionnaire, now time.Time, task core.Task) error {
	if handler.resolver == nil {
		return nil
	}
	block, found, err := handler.resolver.FindVacancy(ctx, payload.Platform)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	missing, err := core.UncoveredQuestions(questionnaire, block)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		if handler.chain == nil {
			return nil
		}
		plan, err := core.ResolveAnswerBlock(questionnaire, block)
		if err != nil {
			return err
		}
		_, err = handler.chain.EnqueueAnswer(ctx, payload.ProfileID, payload.Platform, payload.VacancyExternalID, plan.Answers, definition.LastAttemptFingerprint)
		return err
	}
	if handler.reviews == nil {
		return nil
	}
	return handler.recordReview(ctx, payload, definition, questionnaire, missing[0], now, task)
}

func (handler *VacancyTestCaptureHandler) recordReview(ctx context.Context, payload core.TestCapturePayload, definition core.TestDefinition, questionnaire core.Questionnaire, question core.Question, now time.Time, task core.Task) error {
	sessionID, promptID := vacancyReviewIDs(definition, payload.ProfileID)
	correlationID := task.CorrelationID
	if correlationID == "" {
		correlationID = core.CorrelationID(task.ID)
	}
	session, err := core.NewReviewSession(sessionID, definition, payload.ProfileID, correlationID, now)
	if err != nil {
		return err
	}
	session.Questionnaire = questionnaire
	prompt := core.ReviewPrompt{
		ID: promptID, SessionID: session.ID, Revision: session.Revision, Question: question, CreatedAt: now,
	}
	created, err := handler.reviews.CreateReviewSession(ctx, session)
	if err != nil {
		return err
	}
	if !created {
		// A previous capture already routed this questionnaire. Only continue
		// when the stored session still misses its prompt.
		stored, err := handler.reviews.ReviewSession(ctx, session.ID)
		if err != nil {
			return err
		}
		if stored.Status != core.ReviewPending {
			return nil
		}
		session = stored
	}
	if err := session.WaitForAnswer(prompt, now); err != nil && session.Status != core.ReviewUnsupported {
		return err
	}
	if session.Status != core.ReviewWaiting {
		return nil
	}
	return handler.reviews.SaveReviewPrompt(ctx, session, prompt, session.Revision)
}

func vacancyReviewIDs(definition core.TestDefinition, profileID core.ProfileID) (core.ReviewSessionID, core.ReviewPromptID) {
	digest := sha256.Sum256([]byte(string(definition.ID) + "\x00" + string(profileID) + "\x00" + definition.LastAttemptFingerprint))
	sessionID := core.ReviewSessionID("review-" + hex.EncodeToString(digest[:]))
	return sessionID, core.ReviewPromptID(string(sessionID) + "-prompt-1")
}

func (handler *VacancyTestCaptureHandler) progressiveDefinition(ctx context.Context, payload core.TestCapturePayload, title string, now time.Time) (core.TestDefinition, error) {
	definitions, err := handler.catalog.ListTestDefinitions(ctx, storage.TestDefinitionFilter{Platform: payload.Platform})
	if err != nil {
		return core.TestDefinition{}, err
	}
	externalID := "vacancy:" + payload.VacancyExternalID
	id := core.VacancyTestDefinitionID(payload.Platform, payload.VacancyExternalID)
	for _, definition := range definitions {
		if definition.ID == id {
			return definition, nil
		}
	}
	return core.NewProgressiveTestDefinition(payload.Platform, externalID, title, nil, now)
}
