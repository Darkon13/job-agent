package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
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

func openStore(path string) (*storesqlite.Store, error) {
	if err := storesqlite.MigrateUp(path); err != nil {
		return nil, err
	}
	return storesqlite.Open(path)
}

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

	store, err := openStore(path)
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

	reopened, err := openStore(path)
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

func TestStorePersistsAndRevisionChecksSearchCursor(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	run, err := core.NewSearchRun(
		"golang", "hh-main", "hh", "primary", []core.ProfileID{"primary", "secondary"},
		json.RawMessage(`{"source":"global","text":"Go"}`), "correlation-search", now,
	)
	if err != nil {
		t.Fatalf("new search run: %v", err)
	}
	stored, created, err := store.CreateSearchRun(ctx, run)
	if err != nil || !created || stored.Revision != 1 {
		t.Fatalf("create search run: stored=%#v created=%v err=%v", stored, created, err)
	}
	if err := stored.Advance("1", false, now.Add(time.Minute)); err != nil {
		t.Fatalf("advance search run: %v", err)
	}
	if err := store.SaveSearchRun(ctx, stored, 1); err != nil {
		t.Fatalf("save search run: %v", err)
	}
	if err := store.SaveSearchRun(ctx, stored, 1); !errors.Is(err, storage.ErrRevisionConflict) {
		t.Fatalf("stale save error = %v, want revision conflict", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.SearchRun(ctx, "golang")
	if err != nil {
		t.Fatalf("load persisted search run: %v", err)
	}
	if persisted.Cursor != "1" || persisted.Done || persisted.Revision != 2 || persisted.CorrelationID != "correlation-search" {
		t.Fatalf("unexpected persisted search run: %#v", persisted)
	}
}

func TestStoreReturnsExistingApplicationAndRejectsTaskKeyConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "job-agent.db"))
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

func TestStoreSavesApplicationWithStatusCAS(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "job-agent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	vacancy := core.Vacancy{Platform: "hh", ExternalID: "42", Title: "Go", State: core.VacancyStateOpen, ObservedAt: now}
	if _, err := store.UpsertVacancy(ctx, vacancy); err != nil {
		t.Fatalf("store vacancy: %v", err)
	}
	application, err := core.NewApplication("application-1", core.ApplicationKey{ProfileID: "primary", Vacancy: vacancy.Key()}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, created, err := store.CreateApplication(ctx, application); err != nil || !created {
		t.Fatalf("create application: created=%v err=%v", created, err)
	}

	candidate := application
	if err := candidate.Transition(core.ApplicationPreparing, candidate.UpdatedAt.Add(time.Second)); err != nil {
		t.Fatalf("transition to preparing: %v", err)
	}
	if err := candidate.RecordPreparation("qualified", "rules passed", "resume-1", "Hello", candidate.UpdatedAt.Add(time.Second)); err != nil {
		t.Fatalf("record preparation: %v", err)
	}
	for _, status := range []core.ApplicationStatus{core.ApplicationReady, core.ApplicationSubmitting} {
		if err := candidate.Transition(status, candidate.UpdatedAt.Add(time.Second)); err != nil {
			t.Fatalf("transition to %s: %v", status, err)
		}
	}
	if err := store.SaveApplication(ctx, candidate, core.ApplicationNew); err != nil {
		t.Fatalf("save application: %v", err)
	}
	if err := store.SaveApplication(ctx, application, core.ApplicationNew); !errors.Is(err, storage.ErrRevisionConflict) {
		t.Fatalf("expected status conflict, got %v", err)
	}
	stored, err := store.Application(ctx, application.Key)
	if err != nil {
		t.Fatalf("load application: %v", err)
	}
	if stored.Status != core.ApplicationSubmitting || stored.Attempts != 1 || stored.DecisionCode != "qualified" || stored.PreparedResumeID != "resume-1" || stored.PreparedMessage != "Hello" || stored.PreparedAt == nil {
		t.Fatalf("unexpected stored application: %#v", stored)
	}
}

func TestStoreCountsTasksWithoutReturningPayloads(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "job-agent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for index, taskType := range []core.TaskType{core.TaskResumeTouch, core.TaskResumeTouch, core.TaskApplicationSubmit} {
		priority := core.TaskPriorityNormal
		if index == 1 {
			priority = 50
		}
		task, err := core.NewTask(core.NewTaskParams{
			ID: core.TaskID(fmt.Sprintf("task-%d", index)), Type: taskType,
			IdempotencyKey: fmt.Sprintf("key-%d", index), Source: "test", CorrelationID: "correlation-1",
			Payload: json.RawMessage(`{"secret":"must-not-be-returned"}`), Priority: priority,
		}, now)
		if err != nil {
			t.Fatalf("new task: %v", err)
		}
		if _, err := store.Enqueue(ctx, task); err != nil {
			t.Fatalf("enqueue task: %v", err)
		}
	}
	counts, err := store.TaskCounts(ctx)
	if err != nil {
		t.Fatalf("task counts: %v", err)
	}
	if len(counts) != 3 || counts[0].Count+counts[1].Count+counts[2].Count != 3 {
		t.Fatalf("counts = %#v", counts)
	}
}

func TestStoreListsFailedTasksWithoutCommandPayload(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "job-agent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	task, err := core.NewTask(core.NewTaskParams{
		ID: "failed-1", Type: core.TaskConversationSend, IdempotencyKey: "secret-command-key",
		Source: "test", Platform: "hh", ProfileID: "primary", CorrelationID: "correlation-1",
		Payload: json.RawMessage(`{"text":"must not be returned"}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if _, err := store.Enqueue(ctx, task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	lease, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "worker", TaskType: task.Type, Now: now, LeaseDuration: time.Minute})
	if err != nil || !found {
		t.Fatalf("claim: found=%t err=%v", found, err)
	}
	if err := store.Fail(ctx, lease, &core.OperationError{
		Category: core.ErrorUnsupported, Operation: "conversation.send", Message: "transport unavailable",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("fail: %v", err)
	}

	items, err := store.ListFailedTasks(ctx, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("failed tasks = %#v err=%v", items, err)
	}
	item := items[0]
	if item.ID != task.ID || item.Type != task.Type || item.ProfileID != "primary" || item.Attempts != 1 ||
		item.Failure.Category != core.ErrorUnsupported || item.Failure.Message != "transport unavailable" {
		t.Fatalf("failed task summary = %#v", item)
	}
	stored, err := store.TaskByID(ctx, task.ID)
	if err != nil || string(stored.Payload) != `{"text":"must not be returned"}` {
		t.Fatalf("task by id = %#v err=%v", stored, err)
	}
	if _, err := store.ListFailedTasks(ctx, 0); err == nil {
		t.Fatal("expected invalid limit to fail")
	}
}

func TestStoreCountsApplicationsWithoutReturningDetails(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "job-agent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for index := range 2 {
		vacancy := core.Vacancy{
			Platform: "hh", ExternalID: fmt.Sprint(index), Title: "Vacancy",
			State: core.VacancyStateOpen, ObservedAt: now,
		}
		if _, err := store.UpsertVacancy(ctx, vacancy); err != nil {
			t.Fatalf("create vacancy: %v", err)
		}
		candidate, err := core.NewApplication(core.ApplicationID(fmt.Sprintf("application-%d", index)), core.ApplicationKey{
			ProfileID: "primary", Vacancy: vacancy.Key(),
		}, now)
		if err != nil {
			t.Fatalf("new application: %v", err)
		}
		if _, _, err := store.CreateApplication(ctx, candidate); err != nil {
			t.Fatalf("create application: %v", err)
		}
	}
	counts, err := store.ApplicationCounts(ctx)
	if err != nil {
		t.Fatalf("application counts: %v", err)
	}
	if len(counts) != 1 || counts[0].Status != core.ApplicationNew || counts[0].Count != 2 {
		t.Fatalf("counts = %#v", counts)
	}
}
