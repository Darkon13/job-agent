package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// SearchPageHandler executes exactly one durable search page per task. The
// search run owns the cursor; the task merely carries the cursor it expected.
type SearchPageHandler struct {
	runs      storage.SearchRunRepository
	tasks     broker.TaskQueue
	clock     Clock
	ids       IDGenerator
	mu        sync.RWMutex
	workflows map[core.SearchID]*SearchWorkflow
}

func NewSearchPageHandler(runs storage.SearchRunRepository, tasks broker.TaskQueue, clock Clock, ids IDGenerator) (*SearchPageHandler, error) {
	if runs == nil || tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("search page handler requires all dependencies")
	}
	return &SearchPageHandler{runs: runs, tasks: tasks, clock: clock, ids: ids, workflows: make(map[core.SearchID]*SearchWorkflow)}, nil
}

func (handler *SearchPageHandler) Register(searchID core.SearchID, searchWorkflow *SearchWorkflow) error {
	if searchID == "" || searchWorkflow == nil {
		return errors.New("search workflow registration requires search id and workflow")
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if _, exists := handler.workflows[searchID]; exists {
		return fmt.Errorf("search workflow %s is already registered", searchID)
	}
	handler.workflows[searchID] = searchWorkflow
	return nil
}

// EnsureRun persists the initial state and makes the current cursor runnable.
// Repeating it after a restart is safe because both run and task have stable
// identities independent of generated task IDs.
func (handler *SearchPageHandler) EnsureRun(ctx context.Context, candidate core.SearchRun) (bool, error) {
	stored, created, err := handler.runs.CreateSearchRun(ctx, candidate)
	if err != nil {
		return false, err
	}
	if stored.Done {
		return created, nil
	}
	if _, err := handler.workflow(stored.SearchID); err != nil {
		return false, err
	}
	_, err = handler.enqueue(ctx, stored)
	return created, err
}

func (handler *SearchPageHandler) Handle(ctx context.Context, task core.Task) error {
	if task.Type != core.TaskVacancySearchPage {
		return fmt.Errorf("search page handler cannot process task type %q", task.Type)
	}
	var payload core.SearchPagePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode search page task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	run, err := handler.runs.SearchRun(ctx, payload.SearchID)
	if err != nil {
		return temporarySearchError(run.Platform, "load persistent search run", err)
	}
	if run.Done {
		return nil
	}
	if payload.Cursor != run.Cursor {
		_, err := handler.enqueue(ctx, run)
		return err
	}
	searchWorkflow, err := handler.workflow(run.SearchID)
	if err != nil {
		return err
	}
	result, err := searchWorkflow.RunPage(ctx, SearchRequest{
		SearchID: run.SearchID, Platform: run.Platform, SearchProfileID: run.SearchProfileID,
		TargetProfiles: run.TargetProfiles, Query: run.Query, Cursor: run.Cursor, CorrelationID: run.CorrelationID,
	})
	if err != nil {
		var operationError *core.OperationError
		if errors.As(err, &operationError) {
			return operationError
		}
		return temporarySearchError(run.Platform, "process search page", err)
	}
	expectedRevision := run.Revision
	if err := run.Advance(result.NextCursor, result.Done, handler.clock.Now()); err != nil {
		return err
	}
	if err := handler.runs.SaveSearchRun(ctx, run, expectedRevision); err != nil {
		return temporarySearchError(run.Platform, "save search cursor", err)
	}
	if run.Done {
		return nil
	}
	_, err = handler.enqueue(ctx, run)
	return err
}

func (handler *SearchPageHandler) workflow(searchID core.SearchID) (*SearchWorkflow, error) {
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	searchWorkflow := handler.workflows[searchID]
	if searchWorkflow == nil {
		return nil, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "vacancies.search.route",
			Message: "configured search workflow is unavailable",
		}
	}
	return searchWorkflow, nil
}

func (handler *SearchPageHandler) enqueue(ctx context.Context, run core.SearchRun) (bool, error) {
	payload, err := json.Marshal(core.SearchPagePayload{SearchID: run.SearchID, Cursor: run.Cursor})
	if err != nil {
		return false, err
	}
	idempotencyKey, err := core.SearchPageIdempotencyKey(run.SearchID, run.Cursor)
	if err != nil {
		return false, err
	}
	taskID, err := handler.ids.NewID("task")
	if err != nil {
		return false, err
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(taskID), Type: core.TaskVacancySearchPage, IdempotencyKey: idempotencyKey,
		Source: "search-run", Platform: run.Platform, ProfileID: run.SearchProfileID,
		CorrelationID: run.CorrelationID, Payload: payload,
	}, handler.clock.Now())
	if err != nil {
		return false, err
	}
	created, err := handler.tasks.Enqueue(ctx, task)
	if err != nil {
		return false, temporarySearchError(run.Platform, "enqueue search page", err)
	}
	return created, nil
}

func temporarySearchError(platform core.Platform, message string, cause error) *core.OperationError {
	return &core.OperationError{
		Category: core.ErrorTemporaryFailure, Operation: "vacancies.search.run",
		Platform: platform, Message: message, Cause: cause,
	}
}
