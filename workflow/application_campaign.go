package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type ApplicationCampaignRoute struct {
	SearchID        core.SearchID
	Platform        core.Platform
	SearchProfileID core.ProfileID
	Query           json.RawMessage
	Searcher        adapter.VacancySearcher
}

func (route ApplicationCampaignRoute) Validate() error {
	if route.SearchID == "" || route.Platform == "" || route.SearchProfileID == "" || route.Searcher == nil {
		return errors.New("application campaign route requires search, platform, profile and searcher")
	}
	if len(route.Query) == 0 || !json.Valid(route.Query) {
		return errors.New("application campaign route requires valid JSON query")
	}
	if err := route.Searcher.ValidateSearch(route.Query); err != nil {
		return fmt.Errorf("validate application campaign route %s: %w", route.SearchID, err)
	}
	return nil
}

// campaignMaxLifetime bounds how long a campaign may stay running. A stuck
// in-flight application (a platform block, captcha or pacing wait) otherwise
// keeps the campaign, and every removal guard that checks it, alive forever.
const campaignMaxLifetime = 3 * time.Hour

// ApplicationCampaignHandler executes one bounded reconciliation tick. Search
// pages, applications and tasks are durable, so a failed tick can safely be
// repeated without holding a worker lease for the whole campaign.
type ApplicationCampaignHandler struct {
	campaigns storage.ApplicationCampaignRepository
	vacancies storage.VacancyRepository
	apps      storage.ApplicationRepository
	tasks     broker.TaskStore
	clock     Clock
	ids       IDGenerator
	tickDelay time.Duration
	mu        sync.RWMutex
	routes    map[core.SearchID]ApplicationCampaignRoute
}

func NewApplicationCampaignHandler(
	campaigns storage.ApplicationCampaignRepository,
	vacancies storage.VacancyRepository,
	applications storage.ApplicationRepository,
	tasks broker.TaskStore,
	clock Clock,
	ids IDGenerator,
	tickDelay time.Duration,
) (*ApplicationCampaignHandler, error) {
	if campaigns == nil || vacancies == nil || applications == nil || tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("application campaign handler requires all dependencies")
	}
	if tickDelay <= 0 {
		return nil, errors.New("application campaign handler requires positive tick delay")
	}
	return &ApplicationCampaignHandler{
		campaigns: campaigns, vacancies: vacancies, apps: applications, tasks: tasks,
		clock: clock, ids: ids, tickDelay: tickDelay, routes: make(map[core.SearchID]ApplicationCampaignRoute),
	}, nil
}

func (handler *ApplicationCampaignHandler) Register(route ApplicationCampaignRoute) error {
	if err := route.Validate(); err != nil {
		return err
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if _, exists := handler.routes[route.SearchID]; exists {
		return fmt.Errorf("application campaign route %s is already registered", route.SearchID)
	}
	handler.routes[route.SearchID] = route
	return nil
}

// ReplaceRoutes swaps the whole route set atomically. Configuration reloads use
// it to apply new or changed searches, and routes that disappeared from the
// config are dropped.
func (handler *ApplicationCampaignHandler) ReplaceRoutes(routes []ApplicationCampaignRoute) error {
	replacement := make(map[core.SearchID]ApplicationCampaignRoute, len(routes))
	for _, route := range routes {
		if err := route.Validate(); err != nil {
			return err
		}
		if _, duplicate := replacement[route.SearchID]; duplicate {
			return fmt.Errorf("application campaign routes contain duplicate search %s", route.SearchID)
		}
		replacement[route.SearchID] = route
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	handler.routes = replacement
	return nil
}

func (handler *ApplicationCampaignHandler) Handle(ctx context.Context, task core.Task) error {
	if task.Type != core.TaskApplicationCampaign {
		return fmt.Errorf("application campaign handler cannot process task type %q", task.Type)
	}
	var payload core.ApplicationCampaignPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode application campaign task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}

	campaign, current, err := handler.resolveCampaign(ctx, task, payload)
	if err != nil {
		return err
	}
	if !current {
		return handler.ensureTick(ctx, campaign, 0, task.Priority)
	}
	if campaign.Status != core.ApplicationCampaignRunning {
		return nil
	}
	return handler.runTick(ctx, campaign, task.Priority)
}

func (handler *ApplicationCampaignHandler) resolveCampaign(ctx context.Context, task core.Task, payload core.ApplicationCampaignPayload) (core.ApplicationCampaign, bool, error) {
	if payload.CampaignID != "" {
		campaign, err := handler.campaigns.ApplicationCampaign(ctx, payload.CampaignID)
		if err != nil {
			return core.ApplicationCampaign{}, false, campaignError("load campaign", err)
		}
		return campaign, campaign.Revision == payload.ExpectedRevision, nil
	}
	campaign, err := core.NewApplicationCampaign(core.NewApplicationCampaignParams{
		ID: core.ApplicationCampaignID("campaign-" + string(task.ID)), JobTag: payload.JobTag,
		Profiles: payload.Profiles, Routes: payload.Routes, TargetSuccessful: payload.TargetSuccessful,
		MaxInFlight: payload.MaxInFlight, CorrelationID: task.CorrelationID,
	}, handler.clock.Now())
	if err != nil {
		return core.ApplicationCampaign{}, false, err
	}
	stored, _, err := handler.campaigns.CreateApplicationCampaign(ctx, campaign)
	if err != nil {
		return core.ApplicationCampaign{}, false, campaignError("create campaign", err)
	}
	return stored, stored.Revision == 1, nil
}

func (handler *ApplicationCampaignHandler) runTick(ctx context.Context, campaign core.ApplicationCampaign, priority core.TaskPriority) error {
	states, progress, nextCheckAt, err := handler.progress(ctx, campaign.ID)
	if err != nil {
		return err
	}
	if progress.Submitted >= campaign.TargetSuccessful {
		return handler.stop(ctx, campaign, core.ApplicationCampaignTargetReached, "target successful applications reached")
	}
	if progress.CaptchaBlocked > 0 {
		// The platform demands a human check. Continuing would pile up parked
		// applications, so the campaign fails and waits for the operator.
		return handler.stop(ctx, campaign, core.ApplicationCampaignFailed, "HH требует капчу: введите её и запустите рассылку снова")
	}
	if handler.clock.Now().Sub(campaign.CreatedAt) >= campaignMaxLifetime {
		return handler.stop(ctx, campaign, core.ApplicationCampaignExhausted, "campaign lifetime exceeded")
	}

	scheduled, err := handler.scheduleApplications(ctx, campaign, states, campaign.MaxInFlight-progress.InFlight, priority)
	if err != nil {
		return err
	}
	progress.Planned -= scheduled
	progress.InFlight += scheduled
	if scheduled > 0 {
		nextCheckAt = earlierTime(nextCheckAt, handler.clock.Now().Add(handler.tickDelay))
	}
	if progress.Planned > 0 || progress.InFlight >= campaign.MaxInFlight || (campaign.RouteDone && progress.InFlight > 0) {
		return handler.wait(ctx, campaign, nextCheckAt, priority)
	}
	if campaign.RouteDone {
		if campaign.RouteIndex+1 >= len(campaign.Routes) {
			return handler.stop(ctx, campaign, core.ApplicationCampaignExhausted, "all campaign routes are exhausted")
		}
		expectedRevision := campaign.Revision
		if err := campaign.AdvanceRoute(handler.clock.Now()); err != nil {
			return err
		}
		return handler.saveAndEnqueue(ctx, campaign, expectedRevision, 0, priority)
	}

	route, err := handler.route(campaign.Routes[campaign.RouteIndex])
	if err != nil {
		return err
	}
	page, err := handler.runPage(ctx, campaign, route)
	if err != nil {
		return err
	}
	expectedRevision := campaign.Revision
	if err := campaign.AdvancePage(page.NextCursor, page.Done, handler.clock.Now()); err != nil {
		return err
	}
	if err := handler.campaigns.SaveApplicationCampaign(ctx, campaign, expectedRevision); err != nil {
		return campaignError("save campaign cursor", err)
	}

	states, progress, nextCheckAt, err = handler.progress(ctx, campaign.ID)
	if err != nil {
		return err
	}
	if progress.Submitted >= campaign.TargetSuccessful {
		return handler.stop(ctx, campaign, core.ApplicationCampaignTargetReached, "target successful applications reached")
	}
	scheduled, err = handler.scheduleApplications(ctx, campaign, states, campaign.MaxInFlight-progress.InFlight, priority)
	if err != nil {
		return err
	}
	progress.Planned -= scheduled
	progress.InFlight += scheduled
	if scheduled > 0 {
		nextCheckAt = earlierTime(nextCheckAt, handler.clock.Now().Add(handler.tickDelay))
	}
	delay := time.Duration(0)
	if progress.Planned > 0 || progress.InFlight > 0 {
		delay = handler.delayUntil(nextCheckAt)
	}
	return handler.ensureTick(ctx, campaign, delay, priority)
}

func (handler *ApplicationCampaignHandler) runPage(ctx context.Context, campaign core.ApplicationCampaign, route ApplicationCampaignRoute) (core.SearchPage, error) {
	page, err := route.Searcher.Search(ctx, route.SearchProfileID, route.Query, campaign.Cursor)
	if err != nil {
		var operationError *core.OperationError
		if errors.As(err, &operationError) && operationError.Validate() == nil {
			return core.SearchPage{}, operationError
		}
		return core.SearchPage{}, campaignError("search campaign route", err)
	}
	if err := page.Validate(); err != nil {
		return core.SearchPage{}, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "applications.campaign.search",
			Platform: route.Platform, Message: "campaign route returned an invalid page", Cause: err,
		}
	}
	for _, vacancy := range page.Vacancies {
		if vacancy.Platform != route.Platform {
			return core.SearchPage{}, &core.OperationError{
				Category: core.ErrorPermanentFailure, Operation: "applications.campaign.search",
				Platform: route.Platform, Message: "campaign route returned a vacancy for another platform",
			}
		}
		if _, err := handler.vacancies.UpsertVacancy(ctx, vacancy); err != nil {
			return core.SearchPage{}, campaignError("store campaign vacancy", err)
		}
		_, err := handler.vacancies.RecordDiscovery(ctx, core.VacancyDiscovery{
			VacancyKey: vacancy.Key(), SearchID: route.SearchID,
			ProfileID: route.SearchProfileID, DiscoveredAt: handler.clock.Now(),
		})
		if err != nil {
			return core.SearchPage{}, campaignError("record campaign discovery", err)
		}
		if vacancy.State != core.VacancyStateOpen {
			continue
		}
		for _, profileID := range campaign.Profiles {
			if err := handler.planApplication(ctx, campaign, profileID, vacancy.Key()); err != nil {
				return core.SearchPage{}, err
			}
		}
	}
	return page, nil
}

func (handler *ApplicationCampaignHandler) planApplication(ctx context.Context, campaign core.ApplicationCampaign, profileID core.ProfileID, vacancy core.VacancyKey) error {
	id, err := handler.ids.NewID("application")
	if err != nil {
		return err
	}
	candidate, err := core.NewApplication(core.ApplicationID(id), core.ApplicationKey{ProfileID: profileID, Vacancy: vacancy}, handler.clock.Now())
	if err != nil {
		return err
	}
	application, _, err := handler.apps.CreateApplication(ctx, candidate)
	if errors.Is(err, storage.ErrApplicationRemoved) {
		return nil
	}
	if err != nil {
		return campaignError("create campaign application", err)
	}
	_, err = handler.campaigns.LinkCampaignApplication(ctx, core.CampaignApplication{
		CampaignID: campaign.ID, RouteIndex: campaign.RouteIndex,
		ApplicationID: application.ID, DiscoveredAt: handler.clock.Now(),
	})
	if err != nil {
		return campaignError("link campaign application", err)
	}
	return nil
}

func (handler *ApplicationCampaignHandler) progress(ctx context.Context, campaignID core.ApplicationCampaignID) ([]core.CampaignApplicationState, core.ApplicationCampaignProgress, time.Time, error) {
	states, err := handler.campaigns.ListCampaignApplicationStates(ctx, campaignID)
	if err != nil {
		return nil, core.ApplicationCampaignProgress{}, time.Time{}, campaignError("load campaign applications", err)
	}
	var progress core.ApplicationCampaignProgress
	var nextCheckAt time.Time
	for _, state := range states {
		switch state.Application.Status {
		case core.ApplicationSubmitted:
			progress.Submitted++
			continue
		case core.ApplicationWaitingValidation, core.ApplicationWaitingApproval:
			progress.Blocked++
			if state.Application.DecisionCode == "captcha_required" {
				progress.CaptchaBlocked++
			}
			continue
		case core.ApplicationDryRun, core.ApplicationSkipped:
			progress.Skipped++
			continue
		case core.ApplicationFailed:
			progress.Failed++
			continue
		case core.ApplicationNew, core.ApplicationPreparing, core.ApplicationReady,
			core.ApplicationSubmitting, core.ApplicationPendingReconcile:
		default:
			return nil, core.ApplicationCampaignProgress{}, time.Time{}, fmt.Errorf("unknown application status %q", state.Application.Status)
		}
		task, err := handler.applicationTask(ctx, state.Application)
		if errors.Is(err, broker.ErrTaskNotFound) {
			progress.Planned++
			continue
		}
		if err != nil {
			return nil, core.ApplicationCampaignProgress{}, time.Time{}, campaignError("load application task", err)
		}
		switch task.Status {
		case core.TaskNew, core.TaskProcessing, core.TaskRetryScheduled:
			progress.InFlight++
			checkAt := handler.clock.Now().Add(handler.tickDelay)
			if (task.Status == core.TaskNew || task.Status == core.TaskRetryScheduled) && task.AvailableAt.After(handler.clock.Now()) {
				checkAt = task.AvailableAt
			}
			nextCheckAt = earlierTime(nextCheckAt, checkAt)
		case core.TaskWaitingConfirmation:
			progress.Blocked++
		case core.TaskCompleted, core.TaskFailed:
			progress.Failed++
		default:
			return nil, core.ApplicationCampaignProgress{}, time.Time{}, fmt.Errorf("unknown application task status %q", task.Status)
		}
	}
	return states, progress, nextCheckAt, nil
}

func (handler *ApplicationCampaignHandler) scheduleApplications(ctx context.Context, campaign core.ApplicationCampaign, states []core.CampaignApplicationState, slots int, priority core.TaskPriority) (int, error) {
	if slots <= 0 {
		return 0, nil
	}
	scheduled := 0
	for _, state := range states {
		if scheduled >= slots {
			break
		}
		switch state.Application.Status {
		case core.ApplicationNew, core.ApplicationPreparing, core.ApplicationReady,
			core.ApplicationSubmitting, core.ApplicationPendingReconcile:
		default:
			continue
		}
		_, err := handler.applicationTask(ctx, state.Application)
		if err == nil {
			continue
		}
		if !errors.Is(err, broker.ErrTaskNotFound) {
			return scheduled, campaignError("load application task", err)
		}
		if err := handler.enqueueApplication(ctx, campaign, state.Application, priority); err != nil {
			return scheduled, err
		}
		scheduled++
	}
	return scheduled, nil
}

func (handler *ApplicationCampaignHandler) applicationTask(ctx context.Context, application core.Application) (core.Task, error) {
	key, err := core.ApplicationSubmitIdempotencyKey(application.Key)
	if err != nil {
		return core.Task{}, err
	}
	return handler.tasks.TaskByIdempotencyKey(ctx, key)
}

func (handler *ApplicationCampaignHandler) enqueueApplication(ctx context.Context, campaign core.ApplicationCampaign, application core.Application, priority core.TaskPriority) error {
	payload, err := json.Marshal(core.ApplicationSubmitPayload{ApplicationID: application.ID, Key: application.Key})
	if err != nil {
		return err
	}
	key, err := core.ApplicationSubmitIdempotencyKey(application.Key)
	if err != nil {
		return err
	}
	id, err := handler.ids.NewID("task")
	if err != nil {
		return err
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(id), Type: core.TaskApplicationSubmit, IdempotencyKey: key,
		Source: "application-campaign:" + string(campaign.ID), Platform: application.Key.Vacancy.Platform,
		ProfileID: application.Key.ProfileID, CorrelationID: campaign.CorrelationID, Payload: payload,
		Priority: priority,
	}, handler.clock.Now())
	if err != nil {
		return err
	}
	if _, err := handler.tasks.Enqueue(ctx, task); err != nil {
		return campaignError("enqueue campaign application", err)
	}
	return nil
}

func (handler *ApplicationCampaignHandler) wait(ctx context.Context, campaign core.ApplicationCampaign, nextCheckAt time.Time, priority core.TaskPriority) error {
	expectedRevision := campaign.Revision
	if err := campaign.WaitForApplications(handler.clock.Now()); err != nil {
		return err
	}
	return handler.saveAndEnqueue(ctx, campaign, expectedRevision, handler.delayUntil(nextCheckAt), priority)
}

func (handler *ApplicationCampaignHandler) delayUntil(nextCheckAt time.Time) time.Duration {
	now := handler.clock.Now()
	if nextCheckAt.After(now) {
		return nextCheckAt.Sub(now)
	}
	return handler.tickDelay
}

func earlierTime(current, candidate time.Time) time.Time {
	if current.IsZero() || candidate.Before(current) {
		return candidate
	}
	return current
}

func (handler *ApplicationCampaignHandler) stop(ctx context.Context, campaign core.ApplicationCampaign, status core.ApplicationCampaignStatus, reason string) error {
	expectedRevision := campaign.Revision
	if err := campaign.Stop(status, reason, handler.clock.Now()); err != nil {
		return err
	}
	if err := handler.campaigns.SaveApplicationCampaign(ctx, campaign, expectedRevision); err != nil {
		return campaignError("stop campaign", err)
	}
	return nil
}

func (handler *ApplicationCampaignHandler) saveAndEnqueue(ctx context.Context, campaign core.ApplicationCampaign, expectedRevision uint64, delay time.Duration, priority core.TaskPriority) error {
	if err := handler.campaigns.SaveApplicationCampaign(ctx, campaign, expectedRevision); err != nil {
		return campaignError("save campaign", err)
	}
	return handler.ensureTick(ctx, campaign, delay, priority)
}

func (handler *ApplicationCampaignHandler) ensureTick(ctx context.Context, campaign core.ApplicationCampaign, delay time.Duration, priority core.TaskPriority) error {
	if campaign.Status != core.ApplicationCampaignRunning {
		return nil
	}
	payload, err := json.Marshal(core.NewApplicationCampaignTickPayload(campaign.ID, campaign.Revision))
	if err != nil {
		return err
	}
	key, err := core.ApplicationCampaignTickIdempotencyKey(campaign.ID, campaign.Revision)
	if err != nil {
		return err
	}
	id, err := handler.ids.NewID("task")
	if err != nil {
		return err
	}
	now := handler.clock.Now()
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(id), Type: core.TaskApplicationCampaign, IdempotencyKey: key,
		Source: "cron:" + campaign.JobTag, CorrelationID: campaign.CorrelationID,
		Payload: payload, Priority: priority, AvailableAt: now.Add(delay),
	}, now)
	if err != nil {
		return err
	}
	if _, err := handler.tasks.Enqueue(ctx, task); err != nil {
		return campaignError("enqueue campaign tick", err)
	}
	return nil
}

func (handler *ApplicationCampaignHandler) route(searchID core.SearchID) (ApplicationCampaignRoute, error) {
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	route, exists := handler.routes[searchID]
	if !exists {
		return ApplicationCampaignRoute{}, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "applications.campaign.route",
			Message: "configured campaign route is unavailable",
		}
	}
	return route, nil
}

func campaignError(message string, cause error) *core.OperationError {
	var operationError *core.OperationError
	if errors.As(cause, &operationError) && operationError.Validate() == nil {
		return operationError
	}
	return &core.OperationError{
		Category: core.ErrorTemporaryFailure, Operation: "applications.campaign",
		Message: message, Cause: cause,
	}
}
