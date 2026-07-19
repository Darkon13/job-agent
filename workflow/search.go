package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type SearchRequest struct {
	SearchID        core.SearchID
	Platform        core.Platform
	SearchProfileID core.ProfileID
	TargetProfiles  []core.ProfileID
	Query           json.RawMessage
	Cursor          string
	CorrelationID   core.CorrelationID
}

type SearchResult struct {
	VacanciesSeen       int
	VacanciesCreated    int
	DiscoveriesCreated  int
	ApplicationsCreated int
	TasksCreated        int
	ApplicationsSkipped int
	NextCursor          string
	Done                bool
}

type SearchWorkflow struct {
	searcher     adapter.VacancySearcher
	vacancies    storage.VacancyRepository
	applications storage.ApplicationRepository
	tasks        broker.TaskQueue
	clock        Clock
	ids          IDGenerator
}

func NewSearchWorkflow(
	searcher adapter.VacancySearcher,
	vacancies storage.VacancyRepository,
	applications storage.ApplicationRepository,
	tasks broker.TaskQueue,
	clock Clock,
	ids IDGenerator,
) (*SearchWorkflow, error) {
	if searcher == nil || vacancies == nil || applications == nil || tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("search workflow requires all dependencies")
	}
	return &SearchWorkflow{
		searcher: searcher, vacancies: vacancies, applications: applications,
		tasks: tasks, clock: clock, ids: ids,
	}, nil
}

func (workflow *SearchWorkflow) RunPage(ctx context.Context, request SearchRequest) (SearchResult, error) {
	if err := validateSearchRequest(request); err != nil {
		return SearchResult{}, err
	}
	if err := workflow.searcher.ValidateSearch(request.Query); err != nil {
		return SearchResult{}, fmt.Errorf("validate search %s: %w", request.SearchID, err)
	}

	page, err := workflow.searcher.Search(ctx, request.SearchProfileID, request.Query, request.Cursor)
	if err != nil {
		return SearchResult{}, fmt.Errorf("search %s: %w", request.SearchID, err)
	}
	if err := page.Validate(); err != nil {
		return SearchResult{}, fmt.Errorf("search %s returned invalid page: %w", request.SearchID, err)
	}

	correlationID := request.CorrelationID
	if correlationID == "" {
		value, err := workflow.ids.NewID("correlation")
		if err != nil {
			return SearchResult{}, err
		}
		correlationID = core.CorrelationID(value)
	}
	profiles := uniqueProfiles(request.TargetProfiles)
	result := SearchResult{VacanciesSeen: len(page.Vacancies), NextCursor: page.NextCursor, Done: page.Done}

	for _, vacancy := range page.Vacancies {
		if vacancy.Platform != request.Platform {
			return result, fmt.Errorf("search %s returned vacancy for platform %q, expected %q", request.SearchID, vacancy.Platform, request.Platform)
		}
		created, err := workflow.vacancies.UpsertVacancy(ctx, vacancy)
		if err != nil {
			return result, fmt.Errorf("store vacancy %s: %w", vacancy.Key(), err)
		}
		if created {
			result.VacanciesCreated++
		}

		discovery := core.VacancyDiscovery{
			VacancyKey: vacancy.Key(), SearchID: request.SearchID,
			ProfileID: request.SearchProfileID, DiscoveredAt: workflow.clock.Now(),
		}
		created, err = workflow.vacancies.RecordDiscovery(ctx, discovery)
		if err != nil {
			return result, fmt.Errorf("record vacancy discovery %s: %w", vacancy.Key(), err)
		}
		if created {
			result.DiscoveriesCreated++
		}

		if vacancy.State != core.VacancyStateOpen {
			result.ApplicationsSkipped += len(profiles)
			continue
		}
		for _, profileID := range profiles {
			applicationCreated, taskCreated, err := workflow.planApplication(ctx, correlationID, profileID, vacancy.Key())
			if err != nil {
				return result, err
			}
			if applicationCreated {
				result.ApplicationsCreated++
			}
			if taskCreated {
				result.TasksCreated++
			}
		}
	}
	return result, nil
}

func (workflow *SearchWorkflow) planApplication(
	ctx context.Context,
	correlationID core.CorrelationID,
	profileID core.ProfileID,
	vacancy core.VacancyKey,
) (bool, bool, error) {
	now := workflow.clock.Now()
	applicationID, err := workflow.ids.NewID("application")
	if err != nil {
		return false, false, err
	}
	key := core.ApplicationKey{ProfileID: profileID, Vacancy: vacancy}
	candidate, err := core.NewApplication(core.ApplicationID(applicationID), key, now)
	if err != nil {
		return false, false, err
	}
	stored, applicationCreated, err := workflow.applications.CreateApplication(ctx, candidate)
	if err != nil {
		return false, false, fmt.Errorf("create application %s/%s: %w", profileID, vacancy, err)
	}

	payload := core.ApplicationSubmitPayload{ApplicationID: stored.ID, Key: stored.Key}
	if err := payload.Validate(); err != nil {
		return applicationCreated, false, err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return applicationCreated, false, fmt.Errorf("encode application task: %w", err)
	}
	idempotencyKey, err := core.ApplicationSubmitIdempotencyKey(stored.Key)
	if err != nil {
		return applicationCreated, false, err
	}
	taskID, err := workflow.ids.NewID("task")
	if err != nil {
		return applicationCreated, false, err
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(taskID), Type: core.TaskApplicationSubmit,
		IdempotencyKey: idempotencyKey, Source: "search-discovery",
		Platform: vacancy.Platform, ProfileID: profileID,
		CorrelationID: correlationID, Payload: payloadJSON,
	}, now)
	if err != nil {
		return applicationCreated, false, err
	}
	taskCreated, err := workflow.tasks.Enqueue(ctx, task)
	if err != nil {
		return applicationCreated, false, fmt.Errorf("enqueue application %s: %w", stored.ID, err)
	}
	return applicationCreated, taskCreated, nil
}

func validateSearchRequest(request SearchRequest) error {
	if request.SearchID == "" || request.Platform == "" || request.SearchProfileID == "" {
		return errors.New("search request requires search id, platform and search profile")
	}
	if len(request.Query) == 0 || !json.Valid(request.Query) {
		return errors.New("search request requires valid JSON query")
	}
	if len(request.TargetProfiles) == 0 {
		return errors.New("search request requires target profiles")
	}
	for _, profileID := range request.TargetProfiles {
		if profileID == "" {
			return errors.New("search request contains empty target profile")
		}
	}
	return nil
}

func uniqueProfiles(profiles []core.ProfileID) []core.ProfileID {
	result := make([]core.ProfileID, 0, len(profiles))
	seen := make(map[core.ProfileID]struct{}, len(profiles))
	for _, profileID := range profiles {
		if _, exists := seen[profileID]; exists {
			continue
		}
		seen[profileID] = struct{}{}
		result = append(result, profileID)
	}
	return result
}
