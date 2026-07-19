package memory

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var (
	_ storage.VacancyRepository      = (*Repository)(nil)
	_ storage.ApplicationRepository  = (*Repository)(nil)
	_ storage.TestCatalogRepository  = (*Repository)(nil)
	_ storage.ReviewRepository       = (*Repository)(nil)
	_ storage.ConversationRepository = (*Repository)(nil)
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
	mu                   sync.RWMutex
	vacancies            map[core.VacancyKey]core.Vacancy
	discoveries          map[discoveryKey]core.VacancyDiscovery
	applications         map[core.ApplicationKey]core.Application
	tests                map[core.TestDefinitionID]core.TestDefinition
	reviews              map[core.ReviewSessionID]core.ReviewSession
	prompts              map[core.ReviewPromptID]core.ReviewPrompt
	selections           map[core.ReviewSessionID][]core.ReviewSelection
	conversations        map[core.ConversationID]core.Conversation
	conversationExternal map[conversationExternalKey]core.ConversationID
	messages             map[core.ConversationID]map[core.MessageID]core.ConversationMessage
	followUps            map[core.FollowUpID]core.FollowUp
	followUpKeys         map[string]core.FollowUpID
}

func NewRepository() *Repository {
	return &Repository{
		vacancies:            make(map[core.VacancyKey]core.Vacancy),
		discoveries:          make(map[discoveryKey]core.VacancyDiscovery),
		applications:         make(map[core.ApplicationKey]core.Application),
		tests:                make(map[core.TestDefinitionID]core.TestDefinition),
		reviews:              make(map[core.ReviewSessionID]core.ReviewSession),
		prompts:              make(map[core.ReviewPromptID]core.ReviewPrompt),
		selections:           make(map[core.ReviewSessionID][]core.ReviewSelection),
		conversations:        make(map[core.ConversationID]core.Conversation),
		conversationExternal: make(map[conversationExternalKey]core.ConversationID),
		messages:             make(map[core.ConversationID]map[core.MessageID]core.ConversationMessage),
		followUps:            make(map[core.FollowUpID]core.FollowUp),
		followUpKeys:         make(map[string]core.FollowUpID),
	}
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
	if stored, exists := repository.applications[candidate.Key]; exists {
		return stored, false, nil
	}
	repository.applications[candidate.Key] = candidate
	return candidate, true, nil
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
