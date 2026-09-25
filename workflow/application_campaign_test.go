package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/broker"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

func campaignStartTask(t *testing.T, now time.Time, profiles []core.ProfileID, routes []core.SearchID, target, maxInFlight int) core.Task {
	t.Helper()
	payload, err := json.Marshal(core.NewApplicationCampaignStartPayload("daily", profiles, routes, target, maxInFlight))
	if err != nil {
		t.Fatalf("encode campaign start: %v", err)
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: "start-1", Type: core.TaskApplicationCampaign, IdempotencyKey: "cron-1",
		Source: "cron:daily", CorrelationID: "correlation-1", Payload: payload, Priority: 125,
	}, now)
	if err != nil {
		t.Fatalf("new campaign start task: %v", err)
	}
	return task
}

func campaignTickTask(t *testing.T, queue *brokermemory.Queue, campaignID core.ApplicationCampaignID, revision uint64) core.Task {
	t.Helper()
	key, err := core.ApplicationCampaignTickIdempotencyKey(campaignID, revision)
	if err != nil {
		t.Fatalf("campaign tick key: %v", err)
	}
	task, err := queue.TaskByIdempotencyKey(context.Background(), key)
	if err != nil {
		t.Fatalf("load campaign tick: %v", err)
	}
	return task
}

func submitCampaignApplication(t *testing.T, repository *storagememory.Repository, application core.Application, now time.Time) {
	t.Helper()
	expected := application.Status
	for _, status := range []core.ApplicationStatus{
		core.ApplicationPreparing, core.ApplicationReady, core.ApplicationSubmitting, core.ApplicationSubmitted,
	} {
		if err := application.Transition(status, now); err != nil {
			t.Fatalf("transition application to %s: %v", status, err)
		}
	}
	if err := repository.SaveApplication(context.Background(), application, expected); err != nil {
		t.Fatalf("save submitted application: %v", err)
	}
}

func applicationTasks(tasks []core.Task) []core.Task {
	result := make([]core.Task, 0)
	for _, task := range tasks {
		if task.Type == core.TaskApplicationSubmit {
			result = append(result, task)
		}
	}
	return result
}

func TestApplicationCampaignHandlerLimitsInFlightAndStopsAtTarget(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	ids := &sequentialIDs{}
	searcher := &fakeSearcher{page: core.SearchPage{Done: true, Vacancies: []core.Vacancy{
		{Platform: "hh", ExternalID: "41", Title: "Go 1", State: core.VacancyStateOpen, ObservedAt: now},
		{Platform: "hh", ExternalID: "42", Title: "Go 2", State: core.VacancyStateOpen, ObservedAt: now},
		{Platform: "hh", ExternalID: "43", Title: "Go 3", State: core.VacancyStateOpen, ObservedAt: now},
	}}}
	handler, err := NewApplicationCampaignHandler(repository, repository, repository, queue, fixedClock{now}, ids, time.Second)
	if err != nil {
		t.Fatalf("new campaign handler: %v", err)
	}
	if err := handler.Register(ApplicationCampaignRoute{
		SearchID: "primary", Platform: "hh", SearchProfileID: "profile",
		Query: json.RawMessage(`{"source":"global"}`), Searcher: searcher,
	}); err != nil {
		t.Fatalf("register route: %v", err)
	}
	start := campaignStartTask(t, now, []core.ProfileID{"profile"}, []core.SearchID{"primary"}, 2, 1)
	if err := handler.Handle(ctx, start); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	campaignID := core.ApplicationCampaignID("campaign-" + string(start.ID))
	campaign, err := repository.ApplicationCampaign(ctx, campaignID)
	if err != nil || campaign.Revision != 2 || !campaign.RouteDone {
		t.Fatalf("campaign after first page: %#v err=%v", campaign, err)
	}
	states, err := repository.ListCampaignApplicationStates(ctx, campaignID)
	if err != nil || len(states) != 3 || len(applicationTasks(queue.Tasks())) != 1 {
		t.Fatalf("first tick states=%d application_tasks=%d err=%v", len(states), len(applicationTasks(queue.Tasks())), err)
	}
	if applications := applicationTasks(queue.Tasks()); applications[0].Priority != start.Priority {
		t.Fatalf("application priority=%d, want %d", applications[0].Priority, start.Priority)
	}
	if tick := campaignTickTask(t, queue, campaignID, 2); tick.Priority != start.Priority {
		t.Fatalf("campaign tick priority=%d, want %d", tick.Priority, start.Priority)
	}

	submitCampaignApplication(t, repository, states[0].Application, now.Add(time.Second))
	if err := handler.Handle(ctx, campaignTickTask(t, queue, campaignID, 2)); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if tasks := applicationTasks(queue.Tasks()); len(tasks) != 2 {
		t.Fatalf("application tasks after one success = %d, want 2", len(tasks))
	}
	campaign, _ = repository.ApplicationCampaign(ctx, campaignID)
	if campaign.Revision != 3 || campaign.Status != core.ApplicationCampaignRunning {
		t.Fatalf("campaign waiting for second result: %#v", campaign)
	}

	states, _ = repository.ListCampaignApplicationStates(ctx, campaignID)
	submitCampaignApplication(t, repository, states[1].Application, now.Add(2*time.Second))
	if err := handler.Handle(ctx, campaignTickTask(t, queue, campaignID, 3)); err != nil {
		t.Fatalf("target tick: %v", err)
	}
	campaign, _ = repository.ApplicationCampaign(ctx, campaignID)
	if campaign.Status != core.ApplicationCampaignTargetReached || campaign.Revision != 4 {
		t.Fatalf("completed campaign: %#v", campaign)
	}
	if tasks := applicationTasks(queue.Tasks()); len(tasks) != 2 {
		t.Fatalf("target stop scheduled %d application tasks, want 2", len(tasks))
	}
}

func TestApplicationCampaignHandlerSchedulesFreshestCandidateFirst(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	oldPublished := now.Add(-48 * time.Hour)
	newPublished := now.Add(-time.Hour)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	searcher := &fakeSearcher{page: core.SearchPage{Done: true, Vacancies: []core.Vacancy{
		{Platform: "hh", ExternalID: "old", Title: "Old", State: core.VacancyStateOpen, PublishedAt: &oldPublished, ObservedAt: now},
		{Platform: "hh", ExternalID: "new", Title: "New", State: core.VacancyStateOpen, PublishedAt: &newPublished, ObservedAt: now},
	}}}
	handler, err := NewApplicationCampaignHandler(repository, repository, repository, queue, fixedClock{now}, &sequentialIDs{}, time.Second)
	if err != nil {
		t.Fatalf("new campaign handler: %v", err)
	}
	if err := handler.Register(ApplicationCampaignRoute{
		SearchID: "primary", Platform: "hh", SearchProfileID: "profile", Query: json.RawMessage(`{}`), Searcher: searcher,
	}); err != nil {
		t.Fatalf("register route: %v", err)
	}
	start := campaignStartTask(t, now, []core.ProfileID{"profile"}, []core.SearchID{"primary"}, 1, 1)
	if err := handler.Handle(ctx, start); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	tasks := applicationTasks(queue.Tasks())
	if len(tasks) != 1 {
		t.Fatalf("application tasks=%d, want 1", len(tasks))
	}
	var payload core.ApplicationSubmitPayload
	if err := json.Unmarshal(tasks[0].Payload, &payload); err != nil {
		t.Fatalf("decode application task: %v", err)
	}
	if payload.Key.Vacancy.ExternalID != "new" {
		t.Fatalf("scheduled vacancy=%q, want freshest candidate", payload.Key.Vacancy.ExternalID)
	}
}

func TestApplicationCampaignHandlerAdvancesFallbackAndExhausts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	ids := &sequentialIDs{}
	primary := &fakeSearcher{page: core.SearchPage{Done: true}}
	fallback := &fakeSearcher{page: core.SearchPage{Done: true}}
	handler, err := NewApplicationCampaignHandler(repository, repository, repository, queue, fixedClock{now}, ids, time.Second)
	if err != nil {
		t.Fatalf("new campaign handler: %v", err)
	}
	for searchID, searcher := range map[core.SearchID]*fakeSearcher{"primary": primary, "fallback": fallback} {
		if err := handler.Register(ApplicationCampaignRoute{
			SearchID: searchID, Platform: "hh", SearchProfileID: "profile",
			Query: json.RawMessage(`{}`), Searcher: searcher,
		}); err != nil {
			t.Fatalf("register route %s: %v", searchID, err)
		}
	}
	start := campaignStartTask(t, now, []core.ProfileID{"profile"}, []core.SearchID{"primary", "fallback"}, 1, 1)
	if err := handler.Handle(ctx, start); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	campaignID := core.ApplicationCampaignID("campaign-" + string(start.ID))
	for _, revision := range []uint64{2, 3, 4} {
		if err := handler.Handle(ctx, campaignTickTask(t, queue, campaignID, revision)); err != nil {
			t.Fatalf("campaign revision %d: %v", revision, err)
		}
	}
	campaign, err := repository.ApplicationCampaign(ctx, campaignID)
	if err != nil || campaign.Status != core.ApplicationCampaignExhausted || campaign.RouteIndex != 1 || campaign.Revision != 5 {
		t.Fatalf("exhausted campaign: %#v err=%v", campaign, err)
	}
	if primary.searchCalls != 1 || fallback.searchCalls != 1 {
		t.Fatalf("search calls primary=%d fallback=%d", primary.searchCalls, fallback.searchCalls)
	}
}

func TestApplicationCampaignHandlerAlignsNextTickWithApplicationRetry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	handler, err := NewApplicationCampaignHandler(repository, repository, repository, queue, fixedClock{now}, &sequentialIDs{}, time.Second)
	if err != nil {
		t.Fatalf("new campaign handler: %v", err)
	}
	searcher := &fakeSearcher{page: core.SearchPage{Done: true, Vacancies: []core.Vacancy{{
		Platform: "hh", ExternalID: "42", Title: "Go", State: core.VacancyStateOpen, ObservedAt: now,
	}}}}
	if err := handler.Register(ApplicationCampaignRoute{
		SearchID: "primary", Platform: "hh", SearchProfileID: "profile", Query: json.RawMessage(`{}`), Searcher: searcher,
	}); err != nil {
		t.Fatalf("register route: %v", err)
	}
	start := campaignStartTask(t, now, []core.ProfileID{"profile"}, []core.SearchID{"primary"}, 1, 1)
	if err := handler.Handle(ctx, start); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	lease, found, err := queue.Claim(ctx, broker.ClaimParams{
		WorkerID: "application-worker", TaskType: core.TaskApplicationSubmit,
		Now: now, LeaseDuration: time.Minute,
	})
	if err != nil || !found {
		t.Fatalf("claim application: found=%v err=%v", found, err)
	}
	retryAt := now.Add(time.Hour)
	operationError := &core.OperationError{
		Category: core.ErrorQuotaExceeded, Operation: "applications.budget.reserve",
		Platform: "hh", Message: "daily budget exhausted", RetryAfter: &retryAt,
	}
	if err := queue.Retry(ctx, lease, operationError, retryAt, now.Add(time.Second)); err != nil {
		t.Fatalf("schedule application retry: %v", err)
	}
	campaignID := core.ApplicationCampaignID("campaign-" + string(start.ID))
	if err := handler.Handle(ctx, campaignTickTask(t, queue, campaignID, 2)); err != nil {
		t.Fatalf("reconcile delayed application: %v", err)
	}
	next := campaignTickTask(t, queue, campaignID, 3)
	if !next.AvailableAt.Equal(retryAt) {
		t.Fatalf("next campaign tick available_at=%s, want %s", next.AvailableAt, retryAt)
	}
}

func TestApplicationCampaignHandlerRecoversCursorSaveBeforeTickEnqueue(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	inner := brokermemory.NewQueue()
	queue := &campaignFailOnceQueue{inner: inner}
	ids := &sequentialIDs{}
	searcher := &fakeSearcher{page: core.SearchPage{NextCursor: "page-2"}}
	handler, err := NewApplicationCampaignHandler(repository, repository, repository, queue, fixedClock{now}, ids, time.Second)
	if err != nil {
		t.Fatalf("new campaign handler: %v", err)
	}
	if err := handler.Register(ApplicationCampaignRoute{
		SearchID: "primary", Platform: "hh", SearchProfileID: "profile", Query: json.RawMessage(`{}`), Searcher: searcher,
	}); err != nil {
		t.Fatalf("register route: %v", err)
	}
	start := campaignStartTask(t, now, []core.ProfileID{"profile"}, []core.SearchID{"primary"}, 1, 1)
	if err := handler.Handle(ctx, start); err == nil {
		t.Fatal("expected tick enqueue failure")
	}
	campaignID := core.ApplicationCampaignID("campaign-" + string(start.ID))
	campaign, _ := repository.ApplicationCampaign(ctx, campaignID)
	if campaign.Cursor != "page-2" || campaign.Revision != 2 {
		t.Fatalf("cursor was not saved before enqueue failure: %#v", campaign)
	}
	if err := handler.Handle(ctx, start); err != nil {
		t.Fatalf("retry start task: %v", err)
	}
	if _, err := inner.TaskByIdempotencyKey(ctx, mustCampaignTickKey(t, campaignID, 2)); err != nil {
		t.Fatalf("missing recovered current tick: %v", err)
	}
	if searcher.searchCalls != 1 {
		t.Fatalf("stale retry repeated saved page %d times", searcher.searchCalls)
	}
}

type campaignFailOnceQueue struct {
	inner  *brokermemory.Queue
	failed bool
}

func (queue *campaignFailOnceQueue) Enqueue(ctx context.Context, task core.Task) (bool, error) {
	if task.Type == core.TaskApplicationCampaign && !queue.failed {
		queue.failed = true
		return false, context.DeadlineExceeded
	}
	return queue.inner.Enqueue(ctx, task)
}

func (queue *campaignFailOnceQueue) TaskByIdempotencyKey(ctx context.Context, key string) (core.Task, error) {
	return queue.inner.TaskByIdempotencyKey(ctx, key)
}

func mustCampaignTickKey(t *testing.T, campaignID core.ApplicationCampaignID, revision uint64) string {
	t.Helper()
	key, err := core.ApplicationCampaignTickIdempotencyKey(campaignID, revision)
	if err != nil {
		t.Fatalf("campaign tick key: %v", err)
	}
	return key
}

func TestApplicationCampaignStopsAfterItsLifetime(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	clock := &mutableClock{now: now}
	searcher := &fakeSearcher{page: core.SearchPage{Done: true, Vacancies: []core.Vacancy{
		{Platform: "hh", ExternalID: "v1", Title: "Vacancy", State: core.VacancyStateOpen, ObservedAt: now},
	}}}
	handler, err := NewApplicationCampaignHandler(repository, repository, repository, queue, clock, &sequentialIDs{}, time.Second)
	if err != nil {
		t.Fatalf("new campaign handler: %v", err)
	}
	if err := handler.Register(ApplicationCampaignRoute{
		SearchID: "primary", Platform: "hh", SearchProfileID: "profile", Query: json.RawMessage(`{}`), Searcher: searcher,
	}); err != nil {
		t.Fatalf("register route: %v", err)
	}
	start := campaignStartTask(t, now, []core.ProfileID{"profile"}, []core.SearchID{"primary"}, 5, 1)
	if err := handler.Handle(ctx, start); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	campaignID := core.ApplicationCampaignID("campaign-" + string(start.ID))
	campaign, err := repository.ApplicationCampaign(ctx, campaignID)
	if err != nil {
		t.Fatalf("load campaign: %v", err)
	}
	if campaign.Status != core.ApplicationCampaignRunning {
		t.Fatalf("campaign status = %q", campaign.Status)
	}

	// The scheduled application never leaves the queue; a campaign that old is
	// stuck and must reach a terminal state anyway.
	clock.now = now.Add(campaignMaxLifetime + time.Minute)
	if err := handler.Handle(ctx, campaignTickTask(t, queue, campaignID, campaign.Revision)); err != nil {
		t.Fatalf("tick campaign: %v", err)
	}
	campaign, err = repository.ApplicationCampaign(ctx, campaignID)
	if err != nil {
		t.Fatalf("reload campaign: %v", err)
	}
	if campaign.Status != core.ApplicationCampaignExhausted || campaign.StopReason != "campaign lifetime exceeded" {
		t.Fatalf("stalled campaign = %#v", campaign)
	}
}

func TestApplicationCampaignStopsOnPlatformCaptcha(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	clock := &mutableClock{now: now}
	searcher := &fakeSearcher{page: core.SearchPage{Done: true, Vacancies: []core.Vacancy{
		{Platform: "hh", ExternalID: "v1", Title: "Vacancy", State: core.VacancyStateOpen, ObservedAt: now},
	}}}
	handler, err := NewApplicationCampaignHandler(repository, repository, repository, queue, clock, &sequentialIDs{}, time.Second)
	if err != nil {
		t.Fatalf("new campaign handler: %v", err)
	}
	if err := handler.Register(ApplicationCampaignRoute{
		SearchID: "primary", Platform: "hh", SearchProfileID: "profile", Query: json.RawMessage(`{}`), Searcher: searcher,
	}); err != nil {
		t.Fatalf("register route: %v", err)
	}
	start := campaignStartTask(t, now, []core.ProfileID{"profile"}, []core.SearchID{"primary"}, 5, 1)
	if err := handler.Handle(ctx, start); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	campaignID := core.ApplicationCampaignID("campaign-" + string(start.ID))
	campaign, err := repository.ApplicationCampaign(ctx, campaignID)
	if err != nil {
		t.Fatalf("load campaign: %v", err)
	}
	var submitPayload core.ApplicationSubmitPayload
	for _, queued := range queue.Tasks() {
		if queued.Type == core.TaskApplicationSubmit {
			if err := json.Unmarshal(queued.Payload, &submitPayload); err != nil {
				t.Fatalf("decode submit payload: %v", err)
			}
		}
	}
	if submitPayload.ApplicationID == "" {
		t.Fatalf("campaign scheduled no application: %#v", queue.Tasks())
	}
	application, err := repository.ApplicationByID(ctx, submitPayload.ApplicationID)
	if err != nil {
		t.Fatalf("load scheduled application: %v", err)
	}
	if err := application.Transition(core.ApplicationPreparing, now); err != nil {
		t.Fatalf("preparing: %v", err)
	}
	if err := repository.SaveApplication(ctx, application, core.ApplicationNew); err != nil {
		t.Fatalf("save preparing: %v", err)
	}
	application.DecisionCode = "captcha_required"
	if err := application.Transition(core.ApplicationWaitingValidation, now); err != nil {
		t.Fatalf("waiting validation: %v", err)
	}
	if err := repository.SaveApplication(ctx, application, core.ApplicationPreparing); err != nil {
		t.Fatalf("save parked: %v", err)
	}

	if err := handler.Handle(ctx, campaignTickTask(t, queue, campaignID, campaign.Revision)); err != nil {
		t.Fatalf("tick campaign: %v", err)
	}
	campaign, err = repository.ApplicationCampaign(ctx, campaignID)
	if err != nil {
		t.Fatalf("reload campaign: %v", err)
	}
	if campaign.Status != core.ApplicationCampaignFailed || campaign.StopReason == "" {
		t.Fatalf("campaign = %#v", campaign)
	}
}
