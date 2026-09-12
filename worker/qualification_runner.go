package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func (registry *QualificationAttemptRegistry) Count() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.services)
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
// platform qualification family/level, including appended human revisions.
type QualificationAnswerBlocks interface {
	FindQualificationLevel(ctx context.Context, platform core.Platform, familyID, levelID string) (core.AnswerBlock, bool, error)
}

// QualificationAnswerModel resolves one unknown question through the
// configured provider. The returned answer already passed the operator's local
// validator, so the runner may submit it; provenance travels with the answer.
type QualificationAnswerModel interface {
	Tag() string
	Resolve(ctx context.Context, platform core.Platform, question core.Question) (core.StoredAnswer, error)
}

// QualificationStartHandler runs an explicitly started qualification attempt
// and answers it from a reusable reviewed block. Unknown questions are either
// resolved by the configured answer model or finish the attempt early with a
// review session; a guessed answer is never submitted.
type QualificationStartHandler struct {
	registry     QualificationAttemptRegistryReader
	catalog      storage.QualificationCatalogRepository
	results      storage.QualificationRepository
	tests        storage.TestCatalogRepository
	blocks       QualificationAnswerBlocks
	reviews      storage.ReviewRepository
	revisions    storage.AnswerBlockRevisionRepository
	answerModels map[core.ProfileID]QualificationAnswerModel
	clock        Clock
}

type QualificationAttemptRegistryReader interface {
	Resolve(profileID core.ProfileID) (adapter.QualificationAttemptService, error)
}

func NewQualificationStartHandler(registry QualificationAttemptRegistryReader, catalog storage.QualificationCatalogRepository, results storage.QualificationRepository, tests storage.TestCatalogRepository, blocks QualificationAnswerBlocks, reviews storage.ReviewRepository, revisions storage.AnswerBlockRevisionRepository, clock Clock) (*QualificationStartHandler, error) {
	if registry == nil || catalog == nil || results == nil || tests == nil || blocks == nil || clock == nil {
		return nil, errors.New("qualification start handler requires registry, catalog, results, test catalog, blocks and clock")
	}
	return &QualificationStartHandler{registry: registry, catalog: catalog, results: results, tests: tests, blocks: blocks, reviews: reviews, revisions: revisions, clock: clock}, nil
}

// ConfigureAnswerModels attaches the profile-scoped auto-answer fallback. A
// profile without a model keeps the reviewed-block-only behaviour.
func (handler *QualificationStartHandler) ConfigureAnswerModels(models map[core.ProfileID]QualificationAnswerModel) {
	if handler == nil || len(models) == 0 {
		return
	}
	handler.answerModels = models
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
	block, found, err := handler.blocks.FindQualificationLevel(ctx, payload.Platform, offering.Qualification.FamilyID, offering.Qualification.LevelID)
	if err != nil {
		return err
	}
	model := handler.answerModels[payload.ProfileID]
	if !found && model == nil {
		return qualificationStartError("no reviewed answer block for this level; run a reviewed attempt first")
	}
	if !found {
		qualification := offering.Qualification
		block = core.AnswerBlock{
			Tag:  core.QualificationReviewedBlockTag(payload.Platform, qualification.FamilyID, qualification.LevelID),
			Name: strings.TrimSpace(qualification.FamilyName + " " + qualification.LevelName),
			Kind: core.AnswerBlockQualification, Platform: payload.Platform, Qualification: &qualification,
		}
	}
	service, err := handler.registry.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	return handler.runAttempt(ctx, service, payload, offering, block, model, task.CorrelationID)
}

func (handler *QualificationStartHandler) runAttempt(ctx context.Context, service adapter.QualificationAttemptService, payload core.SkillVerificationStartPayload, offering core.QualificationOffering, block core.AnswerBlock, model QualificationAnswerModel, correlationID core.CorrelationID) error {
	now := handler.clock.Now()
	session, err := service.StartQualification(ctx, payload.ProfileID, payload.OfferingID)
	if err != nil {
		return err
	}
	definition, err := handler.progressiveDefinition(ctx, offering, now)
	if err != nil {
		return err
	}
	modelAnswers := make([]core.StoredAnswer, 0)
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
		if err != nil && model != nil {
			if stored, modelErr := model.Resolve(ctx, payload.Platform, question); modelErr == nil {
				if candidate, resolveErr := core.ResolveStoredAnswer(question, stored); resolveErr == nil {
					resolved = candidate
					err = nil
					modelAnswers = append(modelAnswers, stored)
				} else {
					err = resolveErr
				}
			} else {
				err = modelErr
			}
		}
		if err != nil {
			if handler.reviews != nil {
				if reviewErr := handler.recordReview(ctx, payload, offering, definition, question, fingerprint, block.Tag, correlationID); reviewErr != nil {
					return reviewErr
				}
			}
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
		attempt.Result.AnswerBlockTag = block.Tag
		if result.Passed() && len(modelAnswers) > 0 {
			if err := handler.persistModelAnswers(ctx, block, modelAnswers, model, handler.clock.Now()); err != nil {
				return err
			}
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

// persistModelAnswers extends the qualification block with the answers the
// platform just confirmed. Only a passed attempt reaches this path, so the
// appended answers carry verified model provenance and become reusable.
func (handler *QualificationStartHandler) persistModelAnswers(ctx context.Context, block core.AnswerBlock, answers []core.StoredAnswer, model QualificationAnswerModel, now time.Time) error {
	if handler.revisions == nil || model == nil {
		return nil
	}
	merged := make([]core.StoredAnswer, 0, len(block.Answers)+len(answers))
	position := make(map[string]int, len(block.Answers))
	for _, answer := range block.Answers {
		position[core.NormalizeQuestionText(answer.Question)] = len(merged)
		merged = append(merged, answer)
	}
	for _, answer := range answers {
		verified := answer
		if verified.Provenance != nil {
			provenance := *verified.Provenance
			provenance.Verified = true
			verified.Provenance = &provenance
		}
		key := core.NormalizeQuestionText(verified.Question)
		if index, exists := position[key]; exists {
			merged[index] = verified
			continue
		}
		position[key] = len(merged)
		merged = append(merged, verified)
	}
	latest, exists, err := handler.revisions.LatestAnswerBlockRevision(ctx, block.Tag)
	if err != nil {
		return err
	}
	if exists {
		latestDigest, err := core.StoredAnswersDigest(latest.Answers)
		if err != nil {
			return err
		}
		nextDigest, err := core.StoredAnswersDigest(merged)
		if err != nil {
			return err
		}
		if latestDigest == nextDigest {
			return nil
		}
	}
	name := block.Name
	if strings.TrimSpace(name) == "" {
		name = block.Tag
	}
	_, err = handler.revisions.AppendAnswerBlockRevision(ctx, core.AnswerBlockRevision{
		BlockTag: block.Tag, Name: name, Kind: core.AnswerBlockQualification, Platform: block.Platform,
		Source: "model:" + model.Tag(), Answers: merged, CreatedAt: now,
	})
	return err
}

// recordReview stores a durable review session for the unknown question. Human
// answers extend the block named by AnswerBlockTag, and a later start attempt
// replays them.
func (handler *QualificationStartHandler) recordReview(ctx context.Context, payload core.SkillVerificationStartPayload, offering core.QualificationOffering, definition core.TestDefinition, question core.Question, fingerprint string, blockTag string, correlationID core.CorrelationID) error {
	now := handler.clock.Now()
	sessionID, promptID := qualificationReviewIDs(definition, payload.ProfileID, fingerprint)
	session, err := core.NewReviewSession(sessionID, definition, payload.ProfileID, correlationID, now)
	if err != nil {
		return err
	}
	session.Questionnaire = core.Questionnaire{Title: definition.Title, Questions: []core.Question{question}}
	if tag := strings.TrimSpace(blockTag); tag != "" {
		session.AnswerBlockTag = tag
	} else {
		session.AnswerBlockTag = core.QualificationReviewedBlockTag(
			offering.Platform, offering.Qualification.FamilyID, offering.Qualification.LevelID,
		)
	}
	prompt := core.ReviewPrompt{ID: promptID, SessionID: session.ID, Revision: session.Revision, Question: question, CreatedAt: now}
	created, err := handler.reviews.CreateReviewSession(ctx, session)
	if err != nil || !created {
		return err
	}
	if err := session.WaitForAnswer(prompt, now); err != nil {
		return err
	}
	return handler.reviews.SaveReviewPrompt(ctx, session, prompt, session.Revision)
}

func qualificationReviewIDs(definition core.TestDefinition, profileID core.ProfileID, fingerprint string) (core.ReviewSessionID, core.ReviewPromptID) {
	digest := sha256.Sum256([]byte(string(definition.ID) + "\x00" + string(profileID) + "\x00" + fingerprint))
	sessionID := core.ReviewSessionID("review-" + hex.EncodeToString(digest[:]))
	return sessionID, core.ReviewPromptID(string(sessionID) + "-prompt-1")
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
