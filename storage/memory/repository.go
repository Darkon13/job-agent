package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var (
	_ storage.VacancyRepository                  = (*Repository)(nil)
	_ storage.SearchRunRepository                = (*Repository)(nil)
	_ storage.ApplicationCampaignRepository      = (*Repository)(nil)
	_ storage.ApplicationRepository              = (*Repository)(nil)
	_ storage.ApplicationReadRepository          = (*Repository)(nil)
	_ storage.ApplicationRemovalRepository       = (*Repository)(nil)
	_ storage.ApplicationPlatformStateRepository = (*Repository)(nil)
	_ storage.ApplicationBudgetRepository        = (*Repository)(nil)
	_ storage.ApplicationPaceRepository          = (*Repository)(nil)
	_ storage.ApplicationTailoringRepository     = (*Repository)(nil)
	_ storage.AuthSessionRepository              = (*Repository)(nil)
	_ storage.TestCatalogRepository              = (*Repository)(nil)
	_ storage.ReviewRepository                   = (*Repository)(nil)
	_ storage.ConversationRepository             = (*Repository)(nil)
	_ storage.ProfileStateProposalRepository     = (*Repository)(nil)
	_ storage.ProfileActivityRepository          = (*Repository)(nil)
	_ storage.ProfileActivitySnapshotRepository  = (*Repository)(nil)
)

type discoveryKey struct {
	vacancy core.VacancyKey
	search  core.SearchID
	profile core.ProfileID
}

type conversationExternalKey struct {
	platform   core.Platform
	profileID  core.ProfileID
	externalID string
}

type Repository struct {
	mu                     sync.RWMutex
	vacancies              map[core.VacancyKey]core.Vacancy
	searchRuns             map[core.SearchID]core.SearchRun
	applicationCampaigns   map[core.ApplicationCampaignID]core.ApplicationCampaign
	campaignApplications   map[campaignApplicationKey]core.CampaignApplication
	discoveries            map[discoveryKey]core.VacancyDiscovery
	applications           map[core.ApplicationKey]core.Application
	applicationTombstones  map[core.ApplicationKey]core.ApplicationTombstone
	applicationStates      map[core.ApplicationID]core.ApplicationPlatformState
	applicationBudgets     map[core.ApplicationID]core.ApplicationBudgetReservation
	applicationPacing      map[core.ApplicationID]core.ApplicationPaceReservation
	applicationTailorings  map[core.ApplicationTailoringID]core.ApplicationTailoring
	tailoringApplications  map[core.ApplicationID]core.ApplicationTailoringID
	tailoringProfiles      map[core.ProfileID]core.ApplicationTailoringID
	authSessions           map[core.AuthSessionID]core.AuthSession
	tests                  map[core.TestDefinitionID]core.TestDefinition
	reviews                map[core.ReviewSessionID]core.ReviewSession
	prompts                map[core.ReviewPromptID]core.ReviewPrompt
	selections             map[core.ReviewSessionID][]core.ReviewSelection
	testAttempts           map[testAttemptKey]core.TestAttempt
	answerRevisions        map[string][]core.AnswerBlockRevision
	qualificationAttempts  map[qualificationKey][]core.QualificationAttempt
	qualificationBest      map[qualificationKey]core.QualificationResult
	qualificationOfferings map[qualificationOfferingKey]core.QualificationOffering
	conversations          map[core.ConversationID]core.Conversation
	conversationExternal   map[conversationExternalKey]core.ConversationID
	messages               map[core.ConversationID]map[core.MessageID]core.ConversationMessage
	followUps              map[core.FollowUpID]core.FollowUp
	followUpKeys           map[string]core.FollowUpID
	profileStateProposals  map[core.ProfileStateProposalID]core.ProfileStateProposal
	profileStateKeys       map[string]core.ProfileStateProposalID
	profileStateRevisions  map[core.ProfileStateProposalID]core.ProfileStateRevision
	profileActivity        map[core.ProfileActivityID]core.ProfileActivityRecord
	activitySnapshots      map[core.ProfileActivitySnapshotID]core.ProfileActivitySnapshot
}

func NewRepository() *Repository {
	return &Repository{
		vacancies:              make(map[core.VacancyKey]core.Vacancy),
		searchRuns:             make(map[core.SearchID]core.SearchRun),
		applicationCampaigns:   make(map[core.ApplicationCampaignID]core.ApplicationCampaign),
		campaignApplications:   make(map[campaignApplicationKey]core.CampaignApplication),
		discoveries:            make(map[discoveryKey]core.VacancyDiscovery),
		applications:           make(map[core.ApplicationKey]core.Application),
		applicationTombstones:  make(map[core.ApplicationKey]core.ApplicationTombstone),
		applicationStates:      make(map[core.ApplicationID]core.ApplicationPlatformState),
		applicationBudgets:     make(map[core.ApplicationID]core.ApplicationBudgetReservation),
		applicationPacing:      make(map[core.ApplicationID]core.ApplicationPaceReservation),
		applicationTailorings:  make(map[core.ApplicationTailoringID]core.ApplicationTailoring),
		tailoringApplications:  make(map[core.ApplicationID]core.ApplicationTailoringID),
		tailoringProfiles:      make(map[core.ProfileID]core.ApplicationTailoringID),
		authSessions:           make(map[core.AuthSessionID]core.AuthSession),
		tests:                  make(map[core.TestDefinitionID]core.TestDefinition),
		reviews:                make(map[core.ReviewSessionID]core.ReviewSession),
		prompts:                make(map[core.ReviewPromptID]core.ReviewPrompt),
		selections:             make(map[core.ReviewSessionID][]core.ReviewSelection),
		testAttempts:           make(map[testAttemptKey]core.TestAttempt),
		answerRevisions:        make(map[string][]core.AnswerBlockRevision),
		qualificationAttempts:  make(map[qualificationKey][]core.QualificationAttempt),
		qualificationBest:      make(map[qualificationKey]core.QualificationResult),
		qualificationOfferings: make(map[qualificationOfferingKey]core.QualificationOffering),
		conversations:          make(map[core.ConversationID]core.Conversation),
		conversationExternal:   make(map[conversationExternalKey]core.ConversationID),
		messages:               make(map[core.ConversationID]map[core.MessageID]core.ConversationMessage),
		followUps:              make(map[core.FollowUpID]core.FollowUp),
		followUpKeys:           make(map[string]core.FollowUpID),
		profileStateProposals:  make(map[core.ProfileStateProposalID]core.ProfileStateProposal),
		profileStateKeys:       make(map[string]core.ProfileStateProposalID),
		profileStateRevisions:  make(map[core.ProfileStateProposalID]core.ProfileStateRevision),
		profileActivity:        make(map[core.ProfileActivityID]core.ProfileActivityRecord),
		activitySnapshots:      make(map[core.ProfileActivitySnapshotID]core.ProfileActivitySnapshot),
	}
}

func (repository *Repository) CreateSearchRun(ctx context.Context, candidate core.SearchRun) (core.SearchRun, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.SearchRun{}, false, err
	}
	if err := candidate.Validate(); err != nil {
		return core.SearchRun{}, false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if stored, exists := repository.searchRuns[candidate.SearchID]; exists {
		if stored.Adapter != candidate.Adapter || stored.Platform != candidate.Platform || stored.SearchProfileID != candidate.SearchProfileID ||
			!slices.Equal(stored.TargetProfiles, candidate.TargetProfiles) || !bytes.Equal(stored.Query, candidate.Query) {
			// The configured definition changed: version the run, drop the
			// stale cursor and continue without operator involvement.
			updated := cloneSearchRun(candidate)
			updated.CreatedAt = stored.CreatedAt
			updated.Generation = stored.Generation + 1
			updated.Revision = stored.Revision + 1
			if updated.UpdatedAt.Before(stored.UpdatedAt) {
				updated.UpdatedAt = stored.UpdatedAt
			}
			repository.searchRuns[candidate.SearchID] = updated
			return cloneSearchRun(updated), true, nil
		}
		return cloneSearchRun(stored), false, nil
	}
	repository.searchRuns[candidate.SearchID] = cloneSearchRun(candidate)
	return cloneSearchRun(candidate), true, nil
}

func (repository *Repository) SearchRun(ctx context.Context, searchID core.SearchID) (core.SearchRun, error) {
	if err := ctx.Err(); err != nil {
		return core.SearchRun{}, err
	}
	if searchID == "" {
		return core.SearchRun{}, errors.New("search run requires search id")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	run, exists := repository.searchRuns[searchID]
	if !exists {
		return core.SearchRun{}, errors.New("search run not found")
	}
	return cloneSearchRun(run), nil
}

func (repository *Repository) SaveSearchRun(ctx context.Context, candidate core.SearchRun, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Revision != expectedRevision+1 {
		return errors.New("search run revision must advance by one")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.searchRuns[candidate.SearchID]
	if !exists || stored.Revision != expectedRevision {
		return storage.ErrRevisionConflict
	}
	repository.searchRuns[candidate.SearchID] = cloneSearchRun(candidate)
	return nil
}

func (repository *Repository) UpsertVacancy(ctx context.Context, vacancy core.Vacancy) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := vacancy.Validate(); err != nil {
		return false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	existing, exists := repository.vacancies[vacancy.Key()]
	if exists && existing.ObservedAt.After(vacancy.ObservedAt) {
		return false, nil
	}
	repository.vacancies[vacancy.Key()] = cloneVacancy(vacancy)
	return !exists, nil
}

func (repository *Repository) Vacancy(ctx context.Context, key core.VacancyKey) (core.Vacancy, error) {
	if err := ctx.Err(); err != nil {
		return core.Vacancy{}, err
	}
	if err := key.Validate(); err != nil {
		return core.Vacancy{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	vacancy, exists := repository.vacancies[key]
	if !exists {
		return core.Vacancy{}, errors.New("vacancy not found")
	}
	return cloneVacancy(vacancy), nil
}

func (repository *Repository) RecordDiscovery(ctx context.Context, discovery core.VacancyDiscovery) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := discovery.Validate(); err != nil {
		return false, err
	}
	key := discoveryKey{vacancy: discovery.VacancyKey, search: discovery.SearchID, profile: discovery.ProfileID}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.discoveries[key]; exists {
		return false, nil
	}
	repository.discoveries[key] = discovery
	return true, nil
}

func (repository *Repository) CreateApplication(ctx context.Context, candidate core.Application) (core.Application, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.Application{}, false, err
	}
	if candidate.ID == "" {
		return core.Application{}, false, errors.New("application requires id")
	}
	if err := candidate.Key.Validate(); err != nil {
		return core.Application{}, false, err
	}
	if candidate.Status != core.ApplicationNew || candidate.CreatedAt.IsZero() || candidate.UpdatedAt.IsZero() {
		return core.Application{}, false, errors.New("application repository accepts only initialized new applications")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, removed := repository.applicationTombstones[candidate.Key]; removed {
		return core.Application{}, false, storage.ErrApplicationRemoved
	}
	if stored, exists := repository.applications[candidate.Key]; exists {
		return stored, false, nil
	}
	repository.applications[candidate.Key] = candidate
	return candidate, true, nil
}

func (repository *Repository) ApplicationTombstone(ctx context.Context, id core.ApplicationID) (core.ApplicationTombstone, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.ApplicationTombstone{}, false, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, tombstone := range repository.applicationTombstones {
		if tombstone.ApplicationID == id {
			return tombstone, true, nil
		}
	}
	return core.ApplicationTombstone{}, false, nil
}

func (repository *Repository) RemoveApplication(ctx context.Context, id core.ApplicationID, request core.ApplicationRemoval, removedAt time.Time) (core.ApplicationTombstone, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.ApplicationTombstone{}, false, err
	}
	if id == "" || removedAt.IsZero() {
		return core.ApplicationTombstone{}, false, errors.New("application removal requires id and time")
	}
	if err := request.Reason.Validate(); err != nil {
		return core.ApplicationTombstone{}, false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	application, exists := repository.applicationsByID(id)
	if !exists {
		for _, tombstone := range repository.applicationTombstones {
			if tombstone.ApplicationID == id {
				return tombstone, false, nil
			}
		}
		return core.ApplicationTombstone{}, false, errors.New("application not found")
	}
	switch application.Status {
	case core.ApplicationWaitingValidation, core.ApplicationWaitingApproval, core.ApplicationSubmitted,
		core.ApplicationDryRun, core.ApplicationSkipped, core.ApplicationFailed:
	default:
		return core.ApplicationTombstone{}, false, fmt.Errorf("application %s is still active in status %s", id, application.Status)
	}
	for _, item := range repository.campaignApplications {
		if item.ApplicationID != id {
			continue
		}
		if repository.applicationCampaigns[item.CampaignID].Status == core.ApplicationCampaignRunning {
			return core.ApplicationTombstone{}, false, core.ErrApplicationInRunningCampaign
		}
	}
	if !request.Eligible(application, repository.applicationStates[id], removedAt) {
		return core.ApplicationTombstone{}, false, nil
	}
	if tailoringID, exists := repository.tailoringApplications[id]; exists {
		tailoring := repository.applicationTailorings[tailoringID]
		if tailoring.Status != core.ApplicationTailoringRestored {
			return core.ApplicationTombstone{}, false, core.ErrApplicationActiveTailoringSaga
		}
		delete(repository.applicationTailorings, tailoringID)
		delete(repository.tailoringApplications, id)
	}
	for conversationID, conversation := range repository.conversations {
		if conversation.ApplicationID != id {
			continue
		}
		delete(repository.conversations, conversationID)
		delete(repository.messages, conversationID)
		for followUpID, followUp := range repository.followUps {
			if followUp.ConversationID == conversationID {
				delete(repository.followUps, followUpID)
			}
		}
	}
	delete(repository.applicationStates, id)
	delete(repository.applications, application.Key)
	tombstone := core.ApplicationTombstone{ApplicationID: id, Key: application.Key, Reason: request.Reason, RemovedAt: removedAt.UTC(), Status: application.Status}
	if err := tombstone.Validate(); err != nil {
		return core.ApplicationTombstone{}, false, err
	}
	repository.applicationTombstones[application.Key] = tombstone
	return tombstone, true, nil
}

func (repository *Repository) Application(ctx context.Context, key core.ApplicationKey) (core.Application, error) {
	if err := ctx.Err(); err != nil {
		return core.Application{}, err
	}
	if err := key.Validate(); err != nil {
		return core.Application{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	application, exists := repository.applications[key]
	if !exists {
		return core.Application{}, errors.New("application not found")
	}
	return application, nil
}

func (repository *Repository) SaveApplication(ctx context.Context, candidate core.Application, expectedStatus core.ApplicationStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if candidate.ID == "" || expectedStatus == "" {
		return errors.New("application save requires id and expected status")
	}
	if err := candidate.Key.Validate(); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.applications[candidate.Key]
	if !exists || stored.ID != candidate.ID || stored.Status != expectedStatus {
		return storage.ErrRevisionConflict
	}
	repository.applications[candidate.Key] = candidate
	return nil
}

func (repository *Repository) ApplicationByID(ctx context.Context, id core.ApplicationID) (core.Application, error) {
	if err := ctx.Err(); err != nil {
		return core.Application{}, err
	}
	if id == "" {
		return core.Application{}, errors.New("application id is required")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	application, exists := repository.applicationsByID(id)
	if !exists {
		return core.Application{}, errors.New("application not found")
	}
	return application, nil
}

func (repository *Repository) ListApplications(ctx context.Context, filter storage.ApplicationFilter) ([]core.Application, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filter.Limit < 1 || filter.Limit > 500 {
		return nil, errors.New("application limit must be between 1 and 500")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.Application, 0, min(filter.Limit, len(repository.applications)))
	for _, application := range repository.applications {
		if filter.ProfileID != "" && application.Key.ProfileID != filter.ProfileID {
			continue
		}
		if filter.Status != "" && application.Status != filter.Status {
			continue
		}
		result = append(result, application)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].UpdatedAt.After(result[j].UpdatedAt)
		}
		return result[i].ID > result[j].ID
	})
	if len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (repository *Repository) SaveApplicationPlatformState(ctx context.Context, candidate core.ApplicationPlatformState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.applicationsByID(candidate.ApplicationID); !exists {
		return errors.New("application not found")
	}
	if existing, exists := repository.applicationStates[candidate.ApplicationID]; exists && existing.ObservedAt.After(candidate.ObservedAt) {
		return nil
	}
	repository.applicationStates[candidate.ApplicationID] = cloneApplicationPlatformState(candidate)
	return nil
}

func (repository *Repository) ApplicationPlatformState(ctx context.Context, applicationID core.ApplicationID) (core.ApplicationPlatformState, error) {
	if err := ctx.Err(); err != nil {
		return core.ApplicationPlatformState{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	state, exists := repository.applicationStates[applicationID]
	if !exists {
		return core.ApplicationPlatformState{}, errors.New("application platform state not found")
	}
	return cloneApplicationPlatformState(state), nil
}

func (repository *Repository) ListApplicationsForRetention(ctx context.Context, profileID core.ProfileID) ([]core.Application, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if profileID == "" {
		return nil, errors.New("application retention requires profile")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.Application, 0)
	for _, application := range repository.applications {
		if application.Key.ProfileID == profileID && application.Status == core.ApplicationSubmitted {
			result = append(result, application)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].SubmittedAt, result[j].SubmittedAt
		if (left == nil) != (right == nil) {
			return left == nil
		}
		if left == nil || left.Equal(*right) {
			return result[i].ID < result[j].ID
		}
		return left.Before(*right)
	})
	return result, nil
}

func (repository *Repository) ListStaleValidationApplications(ctx context.Context, profileID core.ProfileID) ([]core.Application, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if profileID == "" {
		return nil, errors.New("application retention requires profile")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.Application, 0)
	for _, application := range repository.applications {
		if application.Key.ProfileID != profileID {
			continue
		}
		if application.Status != core.ApplicationWaitingValidation && application.Status != core.ApplicationSkipped {
			continue
		}
		result = append(result, application)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].UpdatedAt.Before(result[j].UpdatedAt)
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func (repository *Repository) Applications() []core.Application {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.Application, 0, len(repository.applications))
	for _, application := range repository.applications {
		result = append(result, application)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Key.ProfileID != result[j].Key.ProfileID {
			return result[i].Key.ProfileID < result[j].Key.ProfileID
		}
		return result[i].Key.Vacancy.String() < result[j].Key.Vacancy.String()
	})
	return result
}

func (repository *Repository) VacancyCount() int {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return len(repository.vacancies)
}

func (repository *Repository) DiscoveryCount() int {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return len(repository.discoveries)
}

func cloneVacancy(vacancy core.Vacancy) core.Vacancy {
	if vacancy.Attributes == nil {
		return vacancy
	}
	attributes := make(map[string]any, len(vacancy.Attributes))
	for key, value := range vacancy.Attributes {
		attributes[key] = value
	}
	vacancy.Attributes = attributes
	return vacancy
}

func cloneSearchRun(run core.SearchRun) core.SearchRun {
	run.TargetProfiles = append([]core.ProfileID(nil), run.TargetProfiles...)
	run.Query = append([]byte(nil), run.Query...)
	return run
}

func cloneApplicationPlatformState(state core.ApplicationPlatformState) core.ApplicationPlatformState {
	if state.ViewedByOpponent != nil {
		value := *state.ViewedByOpponent
		state.ViewedByOpponent = &value
	}
	if state.PlatformUpdatedAt != nil {
		value := *state.PlatformUpdatedAt
		state.PlatformUpdatedAt = &value
	}
	return state
}
