package sqlite_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
	"github.com/Darkon13/job-agent/workflow"
)

type searcher struct{ page core.SearchPage }

func (searcher) ValidateSearch(json.RawMessage) error { return nil }

func (instance searcher) Search(context.Context, core.ProfileID, json.RawMessage, string) (core.SearchPage, error) {
	return instance.page, nil
}

type clock struct{ now time.Time }

func (clock clock) Now() time.Time { return clock.now }

type ids struct{ next int }

func (generator *ids) NewID(prefix string) (string, error) {
	generator.next++
	return fmt.Sprintf("%s-%d", prefix, generator.next), nil
}

func TestStorePersistsWorkflowStateAcrossReopen(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 123, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	platform := searcher{page: core.SearchPage{Done: true, Vacancies: []core.Vacancy{
		{Platform: "hh", ExternalID: "42", URL: "https://hh.ru/vacancy/42", Title: "Go developer", Employer: "Example", State: core.VacancyStateOpen, ObservedAt: now, Attributes: map[string]any{"area": "1"}},
	}}}

	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	generator := &ids{}
	service, err := workflow.NewSearchWorkflow(platform, store, store, store, clock{now}, generator)
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	request := workflow.SearchRequest{
		SearchID: "golang", Platform: "hh", SearchProfileID: "primary",
		TargetProfiles: []core.ProfileID{"primary", "secondary"}, Query: json.RawMessage(`{"source":"global"}`),
	}
	first, err := service.RunPage(ctx, request)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.VacanciesCreated != 1 || first.DiscoveriesCreated != 1 || first.ApplicationsCreated != 2 || first.TasksCreated != 2 {
		t.Fatalf("unexpected first result: %#v", first)
	}
	before, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("stats before close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	after, err := reopened.Stats(ctx)
	if err != nil {
		t.Fatalf("stats after reopen: %v", err)
	}
	if before != after || after != (storesqlite.Stats{Vacancies: 1, Discoveries: 1, Applications: 2, Tasks: 2}) {
		t.Fatalf("state was not persisted: before=%#v after=%#v", before, after)
	}
	storedVacancy, err := reopened.Vacancy(ctx, core.VacancyKey{Platform: "hh", ExternalID: "42"})
	if err != nil {
		t.Fatalf("load vacancy after reopen: %v", err)
	}
	if storedVacancy.Title != "Go developer" || storedVacancy.Attributes["area"] != "1" || !storedVacancy.ObservedAt.Equal(now) {
		t.Fatalf("vacancy round trip mismatch: %#v", storedVacancy)
	}
	stale := storedVacancy
	stale.Title = "stale title"
	stale.ObservedAt = now.Add(-time.Hour)
	if created, err := reopened.UpsertVacancy(ctx, stale); err != nil || created {
		t.Fatalf("upsert stale vacancy: created=%v err=%v", created, err)
	}
	storedVacancy, err = reopened.Vacancy(ctx, storedVacancy.Key())
	if err != nil || storedVacancy.Title != "Go developer" {
		t.Fatalf("stale observation replaced vacancy: %#v err=%v", storedVacancy, err)
	}

	service, err = workflow.NewSearchWorkflow(platform, reopened, reopened, reopened, clock{now.Add(time.Minute)}, generator)
	if err != nil {
		t.Fatalf("new reopened workflow: %v", err)
	}
	second, err := service.RunPage(ctx, request)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.VacanciesCreated != 0 || second.DiscoveriesCreated != 0 || second.ApplicationsCreated != 0 || second.TasksCreated != 0 {
		t.Fatalf("reopened run was not idempotent: %#v", second)
	}
}

func TestStoreReturnsExistingApplicationAndRejectsTaskKeyConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store, err := storesqlite.Open(filepath.Join(t.TempDir(), "job-agent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	vacancy := core.Vacancy{Platform: "hh", ExternalID: "42", Title: "Go", State: core.VacancyStateOpen, ObservedAt: now}
	if _, err := store.UpsertVacancy(ctx, vacancy); err != nil {
		t.Fatalf("store vacancy: %v", err)
	}
	key := core.ApplicationKey{ProfileID: "primary", Vacancy: vacancy.Key()}
	first, err := core.NewApplication("application-1", key, now)
	if err != nil {
		t.Fatalf("new first application: %v", err)
	}
	stored, created, err := store.CreateApplication(ctx, first)
	if err != nil || !created {
		t.Fatalf("create first application: created=%v err=%v", created, err)
	}
	second, err := core.NewApplication("application-2", key, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("new second application: %v", err)
	}
	existing, created, err := store.CreateApplication(ctx, second)
	if err != nil || created || existing.ID != stored.ID {
		t.Fatalf("existing application mismatch: %#v created=%v err=%v", existing, created, err)
	}

	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-1", Type: core.TaskApplicationSubmit, IdempotencyKey: "stable-key",
		Source: "test", Platform: "hh", ProfileID: "primary", CorrelationID: "correlation-1",
		Payload: json.RawMessage(`{"application_id":"application-1"}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if created, err := store.Enqueue(ctx, task); err != nil || !created {
		t.Fatalf("enqueue first task: created=%v err=%v", created, err)
	}
	conflict := task
	conflict.ID = "task-2"
	conflict.Payload = json.RawMessage(`{"application_id":"different"}`)
	if _, err := store.Enqueue(ctx, conflict); err == nil {
		t.Fatal("expected idempotency conflict")
	}
}
