package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type cursorSearcher struct {
	pages    map[string]core.SearchPage
	failOnce map[string]error
}

func (searcher *cursorSearcher) ValidateSearch(json.RawMessage) error { return nil }

func (searcher *cursorSearcher) Search(_ context.Context, _ core.ProfileID, _ json.RawMessage, cursor string) (core.SearchPage, error) {
	if err := searcher.failOnce[cursor]; err != nil {
		delete(searcher.failOnce, cursor)
		return core.SearchPage{}, err
	}
	return searcher.pages[cursor], nil
}

func newSearchRunFixture(t *testing.T, now time.Time, searcher *cursorSearcher) (*SearchPageHandler, *storagememory.Repository, *brokermemory.Queue, core.SearchRun) {
	t.Helper()
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	ids := &sequentialIDs{}
	searchWorkflow, err := NewSearchWorkflow(searcher, repository, repository, queue, fixedClock{now}, ids)
	if err != nil {
		t.Fatalf("new search workflow: %v", err)
	}
	handler, err := NewSearchPageHandler(repository, queue, fixedClock{now}, ids)
	if err != nil {
		t.Fatalf("new search page handler: %v", err)
	}
	if err := handler.Register("golang", searchWorkflow); err != nil {
		t.Fatalf("register search workflow: %v", err)
	}
	run, err := core.NewSearchRun(
		"golang", "hh-main", "hh", "primary", []core.ProfileID{"primary"},
		json.RawMessage(`{"source":"global"}`), "correlation-search", now,
	)
	if err != nil {
		t.Fatalf("new search run: %v", err)
	}
	return handler, repository, queue, run
}

func searchPageTask(t *testing.T, queue *brokermemory.Queue, searchID core.SearchID, cursor string) core.Task {
	t.Helper()
	key, err := core.SearchPageIdempotencyKey(searchID, cursor)
	if err != nil {
		t.Fatalf("search task key: %v", err)
	}
	task, err := queue.TaskByIdempotencyKey(context.Background(), key)
	if err != nil {
		t.Fatalf("load search task: %v", err)
	}
	return task
}

func TestSearchPageHandlerPersistsCursorAndResumesAfterPartialFailure(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	searcher := &cursorSearcher{
		pages: map[string]core.SearchPage{
			"": {NextCursor: "1", Vacancies: []core.Vacancy{{Platform: "hh", ExternalID: "42", Title: "Go", State: core.VacancyStateOpen, ObservedAt: now}}},
			"1": {Done: true, Vacancies: []core.Vacancy{
				{Platform: "hh", ExternalID: "42", Title: "Go updated", State: core.VacancyStateOpen, ObservedAt: now},
				{Platform: "hh", ExternalID: "43", Title: "Backend", State: core.VacancyStateOpen, ObservedAt: now},
			}},
		},
		failOnce: map[string]error{"1": errors.New("temporary network failure")},
	}
	handler, repository, queue, run := newSearchRunFixture(t, now, searcher)
	if created, err := handler.EnsureRun(context.Background(), run); err != nil || !created {
		t.Fatalf("ensure run: created=%v err=%v", created, err)
	}
	if err := handler.Handle(context.Background(), searchPageTask(t, queue, "golang", "")); err != nil {
		t.Fatalf("first page: %v", err)
	}
	stored, err := repository.SearchRun(context.Background(), "golang")
	if err != nil || stored.Cursor != "1" || stored.Done {
		t.Fatalf("cursor after first page: %#v err=%v", stored, err)
	}
	secondTask := searchPageTask(t, queue, "golang", "1")
	if err := handler.Handle(context.Background(), secondTask); !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("partial failure = %v, want temporary", err)
	}
	stored, _ = repository.SearchRun(context.Background(), "golang")
	if stored.Cursor != "1" || stored.Done {
		t.Fatalf("failed page advanced cursor: %#v", stored)
	}

	// A fresh handler models process restart over the same durable state.
	restartedIDs := &sequentialIDs{next: 100}
	restarted, err := NewSearchPageHandler(repository, queue, fixedClock{now.Add(time.Minute)}, restartedIDs)
	if err != nil {
		t.Fatalf("new restarted handler: %v", err)
	}
	restartedWorkflow, _ := NewSearchWorkflow(searcher, repository, repository, queue, fixedClock{now.Add(time.Minute)}, restartedIDs)
	if err := restarted.Register("golang", restartedWorkflow); err != nil {
		t.Fatalf("register restarted workflow: %v", err)
	}
	if _, err := restarted.EnsureRun(context.Background(), run); err != nil {
		t.Fatalf("ensure restarted run: %v", err)
	}
	if err := restarted.Handle(context.Background(), secondTask); err != nil {
		t.Fatalf("resumed second page: %v", err)
	}
	stored, _ = repository.SearchRun(context.Background(), "golang")
	if !stored.Done || stored.Cursor != "" || repository.VacancyCount() != 2 || repository.DiscoveryCount() != 2 {
		t.Fatalf("unexpected completed state: run=%#v vacancies=%d discoveries=%d", stored, repository.VacancyCount(), repository.DiscoveryCount())
	}
	if applications := repository.Applications(); len(applications) != 2 {
		t.Fatalf("duplicate page produced %d applications, want 2", len(applications))
	}
}
