package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type fakeSearcher struct {
	page        core.SearchPage
	searchCalls int
}

func (searcher *fakeSearcher) ValidateSearch(query json.RawMessage) error {
	if string(query) == `{"invalid":true}` {
		return errors.New("invalid query")
	}
	return nil
}

func (searcher *fakeSearcher) Search(context.Context, core.ProfileID, json.RawMessage, string) (core.SearchPage, error) {
	searcher.searchCalls++
	return searcher.page, nil
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type sequentialIDs struct{ next int }

func (ids *sequentialIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}

type failOnceQueue struct {
	inner  *brokermemory.Queue
	failed bool
}

func (queue *failOnceQueue) Enqueue(ctx context.Context, task core.Task) (bool, error) {
	if !queue.failed {
		queue.failed = true
		return false, errors.New("broker unavailable")
	}
	return queue.inner.Enqueue(ctx, task)
}

func TestSearchWorkflowDistributesOneSearchAcrossProfilesIdempotently(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	searcher := &fakeSearcher{page: core.SearchPage{
		Vacancies: []core.Vacancy{
			{Platform: "hh", ExternalID: "42", Title: "Go developer", State: core.VacancyStateOpen, ObservedAt: now},
			{Platform: "hh", ExternalID: "43", Title: "Archived", State: core.VacancyStateArchived, ObservedAt: now},
		},
		NextCursor: "page-2",
	}}
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	ids := &sequentialIDs{}
	workflow, err := NewSearchWorkflow(searcher, repository, repository, queue, fixedClock{now}, ids)
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	request := SearchRequest{
		SearchID: "golang", Platform: "hh", SearchProfileID: "primary",
		TargetProfiles: []core.ProfileID{"primary", "secondary", "primary"},
		Query:          json.RawMessage(`{"source":"global"}`),
	}

	first, err := workflow.RunPage(context.Background(), request)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.VacanciesCreated != 2 || first.DiscoveriesCreated != 2 || first.ApplicationsCreated != 2 || first.TasksCreated != 2 {
		t.Fatalf("unexpected first result: %#v", first)
	}
	if first.ApplicationsSkipped != 2 || first.NextCursor != "page-2" {
		t.Fatalf("unexpected skip/cursor result: %#v", first)
	}

	second, err := workflow.RunPage(context.Background(), request)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.VacanciesCreated != 0 || second.DiscoveriesCreated != 0 || second.ApplicationsCreated != 0 || second.TasksCreated != 0 {
		t.Fatalf("second run was not idempotent: %#v", second)
	}
	fallbackRequest := request
	fallbackRequest.SearchID = "backend-fallback"
	fallback, err := workflow.RunPage(context.Background(), fallbackRequest)
	if err != nil {
		t.Fatalf("fallback run: %v", err)
	}
	if fallback.DiscoveriesCreated != 2 || fallback.ApplicationsCreated != 0 || fallback.TasksCreated != 0 {
		t.Fatalf("fallback duplicated applications or tasks: %#v", fallback)
	}
	if searcher.searchCalls != 3 || repository.VacancyCount() != 2 || repository.DiscoveryCount() != 4 {
		t.Fatalf("unexpected repository/search counts: calls=%d vacancies=%d discoveries=%d", searcher.searchCalls, repository.VacancyCount(), repository.DiscoveryCount())
	}
	applications := repository.Applications()
	tasks := queue.Tasks()
	if len(applications) != 2 || len(tasks) != 2 {
		t.Fatalf("unexpected applications/tasks: %d/%d", len(applications), len(tasks))
	}
	for _, task := range tasks {
		var payload core.ApplicationSubmitPayload
		if err := json.Unmarshal(task.Payload, &payload); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		if payload.ApplicationID == "" || payload.Key.ProfileID != task.ProfileID || payload.Key.Vacancy.ExternalID != "42" {
			t.Fatalf("unexpected task payload: %#v", payload)
		}
	}
}

func TestSearchWorkflowRejectsInvalidAdapterPageBeforeWrites(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	invalid := core.Vacancy{Platform: "hh", ExternalID: "42", Title: "Go", State: core.VacancyStateOpen, ObservedAt: now}
	searcher := &fakeSearcher{page: core.SearchPage{Vacancies: []core.Vacancy{invalid, invalid}}}
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	workflow, err := NewSearchWorkflow(searcher, repository, repository, queue, fixedClock{now}, &sequentialIDs{})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	_, err = workflow.RunPage(context.Background(), SearchRequest{
		SearchID: "golang", Platform: "hh", SearchProfileID: "primary",
		TargetProfiles: []core.ProfileID{"primary"}, Query: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected invalid page error")
	}
	if repository.VacancyCount() != 0 || len(queue.Tasks()) != 0 {
		t.Fatal("invalid page produced writes")
	}
}

func TestSearchWorkflowRecoversWhenApplicationWasStoredBeforeQueueFailure(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	searcher := &fakeSearcher{page: core.SearchPage{Vacancies: []core.Vacancy{
		{Platform: "hh", ExternalID: "42", Title: "Go", State: core.VacancyStateOpen, ObservedAt: now},
	}}}
	repository := storagememory.NewRepository()
	innerQueue := brokermemory.NewQueue()
	queue := &failOnceQueue{inner: innerQueue}
	workflow, err := NewSearchWorkflow(searcher, repository, repository, queue, fixedClock{now}, &sequentialIDs{})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	request := SearchRequest{
		SearchID: "golang", Platform: "hh", SearchProfileID: "primary",
		TargetProfiles: []core.ProfileID{"primary"}, Query: json.RawMessage(`{}`),
	}
	if _, err := workflow.RunPage(context.Background(), request); err == nil {
		t.Fatal("expected first queue attempt to fail")
	}
	stored := repository.Applications()
	if len(stored) != 1 || len(innerQueue.Tasks()) != 0 {
		t.Fatalf("unexpected partial state: applications=%d tasks=%d", len(stored), len(innerQueue.Tasks()))
	}

	result, err := workflow.RunPage(context.Background(), request)
	if err != nil {
		t.Fatalf("recovery run: %v", err)
	}
	if result.ApplicationsCreated != 0 || result.TasksCreated != 1 {
		t.Fatalf("unexpected recovery result: %#v", result)
	}
	var payload core.ApplicationSubmitPayload
	if err := json.Unmarshal(innerQueue.Tasks()[0].Payload, &payload); err != nil {
		t.Fatalf("decode recovered task: %v", err)
	}
	if payload.ApplicationID != stored[0].ID {
		t.Fatalf("task references %s, stored application is %s", payload.ApplicationID, stored[0].ID)
	}
}

func TestApplicationSubmitIdempotencyDoesNotDependOnRuntimeIDs(t *testing.T) {
	key := core.ApplicationKey{ProfileID: "profile", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"}}
	first, err := core.ApplicationSubmitIdempotencyKey(key)
	if err != nil {
		t.Fatalf("first key: %v", err)
	}
	second, err := core.ApplicationSubmitIdempotencyKey(key)
	if err != nil {
		t.Fatalf("second key: %v", err)
	}
	if first != second {
		t.Fatalf("idempotency keys differ: %s != %s", first, second)
	}
}
