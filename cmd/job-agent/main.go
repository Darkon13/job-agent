package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/adapters/hh"
	"github.com/Darkon13/job-agent/api/httpapi"
	"github.com/Darkon13/job-agent/broker"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	applicationoperator "github.com/Darkon13/job-agent/operator"
	jobscheduler "github.com/Darkon13/job-agent/scheduler"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
	taskworker "github.com/Darkon13/job-agent/worker"
	"github.com/Darkon13/job-agent/workflow"
)

type mainOptions struct {
	configPath string
	migrateUp  bool
}

func main() {
	options, err := parseMainOptions(os.Args[1:])
	if err != nil {
		log.Fatalf("usage: %s [-migrate-up] <config.json>: %v", os.Args[0], err)
	}

	registry := adapter.NewRegistry()
	if err := registry.Register(hh.Name, hh.New); err != nil {
		log.Fatal(err)
	}

	cfg, err := appconfig.Load(options.configPath)
	if err != nil {
		log.Fatal(err)
	}
	if options.migrateUp {
		if err := storesqlite.MigrateUp(cfg.Database.Path); err != nil {
			log.Fatalf("migrate database: %v", err)
		}
	}
	store, err := storesqlite.Open(cfg.Database.Path)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("close database: %v", err)
		}
	}()

	instances := make(map[string]adapter.Adapter, len(cfg.Adapters))
	for _, item := range cfg.Adapters {
		instance, err := registry.Open(item.Type, item.Settings)
		if err != nil {
			log.Fatalf("open adapter %q: %v", item.Tag, err)
		}
		for _, search := range cfg.Searches {
			if search.Adapter == item.Tag {
				if err := instance.ValidateSearch(search.Query); err != nil {
					log.Fatalf("validate search %q: %v", search.Tag, err)
				}
			}
		}
		instances[item.Tag] = instance
	}
	profiles, err := probeProfileAuthorizations(context.Background(), cfg.Profiles, instances)
	if err != nil {
		log.Fatalf("probe profile authorization: %v", err)
	}
	for profileID, runtime := range profiles {
		if runtime.Status == core.ProfileAuthRequired {
			log.Printf("profile %q requires authentication; profile workers are disabled", profileID)
		}
	}

	conversationWorkflow, err := workflow.NewConversationWorkflow(store, store, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	if err != nil {
		log.Fatalf("create conversation workflow: %v", err)
	}
	conversationAPI, err := httpapi.NewConversationAPI(store, conversationWorkflow)
	if err != nil {
		log.Fatalf("create conversation API: %v", err)
	}
	runtimeAPI, err := httpapi.NewRuntimeAPI(store)
	if err != nil {
		log.Fatalf("create runtime API: %v", err)
	}
	profileStateResources, err := cfg.BuildProfileStateResources()
	if err != nil {
		log.Fatalf("build profile state resources: %v", err)
	}
	profileStatePlanner, err := workflow.NewProfileStatePlanner(
		profileStateResources, store, workflow.SystemClock{}, workflow.RandomIDGenerator{},
	)
	if err != nil {
		log.Fatalf("create profile state planner: %v", err)
	}
	profileStateReaders := make(map[core.ProfileID]adapter.ProfileStateReader)
	profileStateProfiles := make(map[core.ProfileID]struct{}, len(profileStateResources))
	for _, resource := range profileStateResources {
		profileStateProfiles[resource.ProfileID] = struct{}{}
	}
	profileStateWriters := taskworker.NewProfileStateWriterRegistry()
	profileStatePlatforms := make(map[core.ProfileID]core.Platform)
	conversationTransports := taskworker.NewConversationTransportRegistry()
	applicationTransports := taskworker.NewApplicationTransportRegistry()
	resumeTouchers := taskworker.NewResumeToucherRegistry()
	applicationPlans := make(taskworker.StaticApplicationPlans)
	for _, profile := range cfg.Profiles {
		preparer, err := applicationPreparer(profile)
		if err != nil {
			log.Fatalf("build application operator for profile %q: %v", profile.Tag, err)
		}
		if !profile.Enabled {
			continue
		}
		profileID := core.ProfileID(profile.Tag)
		runtime := profiles[profileID]
		instance := instances[profile.Adapter]
		apiReady := runtime.Status == core.ProfileEnabled && runtime.Reader != nil
		browserApplicationsReady := false
		if profile.StateFile != "" {
			if binder, ok := instance.(adapter.BrowserSessionBinder); ok {
				reader, err := binder.BindBrowserSession(profileID, profile.StateFile)
				if err != nil {
					log.Fatalf("bind browser session for profile %q: %v", profile.Tag, err)
				}
				runtime.BrowserReader = reader
				profiles[profileID] = runtime
				log.Printf("profile %q has a browser-backed read session", profile.Tag)
				if profileStateReader, ok := instance.(adapter.ProfileStateReader); ok {
					profileStateReaders[profileID] = profileStateReader
				}
			}
			if _, needed := profileStateProfiles[profileID]; needed {
				if binder, ok := instance.(adapter.BrowserProfileStateSessionBinder); ok {
					writer, err := binder.BindBrowserProfileStateSession(profileID, profile.StateFile)
					if err != nil {
						log.Fatalf("bind browser profile state session for profile %q: %v", profile.Tag, err)
					}
					if err := profileStateWriters.Register(profileID, writer); err != nil {
						log.Fatalf("register profile state writer for profile %q: %v", profile.Tag, err)
					}
					profileStatePlatforms[profileID] = core.Platform(instance.Name())
				}
			}
			if !apiReady && profile.Applications.ExecutionMode() != appconfig.ApplicationModeDryRun {
				if binder, ok := instance.(adapter.BrowserApplicationSessionBinder); ok {
					transport, err := binder.BindBrowserApplicationSession(profileID, profile.StateFile, adapter.BrowserApplicationOptions{
						AllowVisibilityChange: profile.Applications.AllowVisibilityChange,
						ResumeID:              profile.Resume,
					})
					if err != nil {
						log.Fatalf("bind browser application session for profile %q: %v", profile.Tag, err)
					}
					if err := applicationTransports.Register(profileID, transport); err != nil {
						log.Fatalf("register browser application transport for profile %q: %v", profile.Tag, err)
					}
					browserApplicationsReady = true
					log.Printf("profile %q uses explicit browser-backed application transport", profile.Tag)
				}
			}
		}
		if apiReady {
			if transport, ok := instance.(adapter.ConversationTransport); ok {
				if err := conversationTransports.Register(profileID, transport); err != nil {
					log.Fatalf("register conversation transport for profile %q: %v", profile.Tag, err)
				}
			}
			if transport, ok := instance.(adapter.ApplicationTransport); ok {
				if err := applicationTransports.Register(profileID, transport); err != nil {
					log.Fatalf("register application transport for profile %q: %v", profile.Tag, err)
				}
			}
		} else if runtime.BrowserReader == nil {
			log.Printf("profile %q has no authorized API session; API workers are disabled", profile.Tag)
		}
		applicationReady := apiReady || browserApplicationsReady || runtime.BrowserReader != nil && profile.Applications.ExecutionMode() == appconfig.ApplicationModeDryRun
		if applicationReady {
			if !apiReady && !browserApplicationsReady {
				if err := applicationTransports.RegisterVacancyReader(profileID, runtime.BrowserReader); err != nil {
					log.Fatalf("register browser vacancy reader for profile %q: %v", profile.Tag, err)
				}
			}
			applicationPlans[profileID] = taskworker.ApplicationPlan{
				ResumeID: profile.Resume, Mode: core.ApplicationExecutionMode(profile.Applications.ExecutionMode()),
				Message: profile.Applications.Message, Preparer: preparer, DailyLimit: profile.Applications.DailyLimit,
				Timezone: profile.Applications.LocationName(),
			}
		}
		if toucher, ok := instance.(adapter.ResumeToucher); ok {
			if err := resumeTouchers.Register(profileID, toucher); err != nil {
				log.Fatalf("register resume toucher for profile %q: %v", profile.Tag, err)
			}
		} else if instance.Name() == hh.Name && profile.StateFile != "" {
			if _, err := os.Stat(profile.StateFile); err == nil {
				toucher, err := hh.NewResumeTouchTransport(profile.StateFile, nil)
				if err != nil {
					log.Fatalf("create HH resume toucher for profile %q: %v", profile.Tag, err)
				}
				if err := resumeTouchers.Register(profileID, toucher); err != nil {
					log.Fatalf("register HH resume toucher for profile %q: %v", profile.Tag, err)
				}
			}
		}
	}
	profileStateApplyWorkflow, err := workflow.NewProfileStateApplyWorkflow(
		store, store, workflow.SystemClock{}, workflow.RandomIDGenerator{}, profileStatePlatforms,
	)
	if err != nil {
		log.Fatalf("create profile state apply workflow: %v", err)
	}
	profileStateAPI, err := httpapi.NewProfileStateAPI(profileStatePlanner, profileStateApplyWorkflow, store, profileStateReaders)
	if err != nil {
		log.Fatalf("create profile state API: %v", err)
	}
	conversationHandlers, err := taskworker.NewConversationHandlers(
		store, conversationWorkflow, conversationTransports, taskworker.StaticMessageResolver{}, taskworker.SystemClock{},
	)
	if err != nil {
		log.Fatalf("create conversation handlers: %v", err)
	}
	workers, err := conversationWorkers(store, conversationHandlers)
	if err != nil {
		log.Fatalf("create conversation workers: %v", err)
	}
	if profileStateWriters.Count() > 0 {
		profileStateHandler, err := taskworker.NewProfileStateApplyHandler(store, profileStateWriters)
		if err != nil {
			log.Fatalf("create profile state apply handler: %v", err)
		}
		profileStateWorker, err := newTaskWorker(store, core.TaskProfileStateApply, profileStateHandler.Handle)
		if err != nil {
			log.Fatalf("create profile state apply worker: %v", err)
		}
		workers = append(workers, profileStateWorker)
	}
	if len(applicationPlans) > 0 && applicationTransports.VacancyReaderCount() > 0 {
		applicationHandler, err := taskworker.NewApplicationHandler(
			store, store, store, applicationTransports, applicationPlans, taskworker.SystemClock{},
		)
		if err != nil {
			log.Fatalf("create application handler: %v", err)
		}
		applicationWorker, err := newTaskWorker(store, core.TaskApplicationSubmit, applicationHandler.Handle)
		if err != nil {
			log.Fatalf("create application worker: %v", err)
		}
		workers = append(workers, applicationWorker)
	}
	if resumeTouchers.Count() > 0 {
		resumeHandler, err := taskworker.NewResumeTouchHandler(resumeTouchers)
		if err != nil {
			log.Fatalf("create resume touch handler: %v", err)
		}
		resumeWorker, err := newTaskWorker(store, core.TaskResumeTouch, resumeHandler.Handle)
		if err != nil {
			log.Fatalf("create resume touch worker: %v", err)
		}
		workers = append(workers, resumeWorker)
	}
	campaignHandler, campaignDefinitions, campaignRoutes, campaignJobs, err := configureApplicationCampaigns(
		cfg, instances, profiles, store,
	)
	if err != nil {
		log.Fatalf("configure application campaigns: %v", err)
	}
	if campaignJobs > 0 {
		campaignWorker, err := newTaskWorker(store, core.TaskApplicationCampaign, campaignHandler.Handle)
		if err != nil {
			log.Fatalf("create application campaign worker: %v", err)
		}
		workers = append(workers, campaignWorker)
	}
	searchHandler, searchRuns, err := configureSearchRuns(context.Background(), cfg, instances, profiles, campaignRoutes, store)
	if err != nil {
		log.Fatalf("configure vacancy searches: %v", err)
	}
	if searchRuns > 0 {
		searchWorker, err := newTaskWorker(store, core.TaskVacancySearchPage, searchHandler.Handle)
		if err != nil {
			log.Fatalf("create vacancy search worker: %v", err)
		}
		workers = append(workers, searchWorker)
	}
	definitions, err := resumeTouchDefinitions(cfg, instances, resumeTouchers)
	if err != nil {
		log.Fatalf("build scheduled jobs: %v", err)
	}
	definitions = append(definitions, campaignDefinitions...)
	scheduler, err := jobscheduler.New(store, store, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	if err != nil {
		log.Fatalf("create scheduler: %v", err)
	}
	if err := scheduler.Sync(context.Background(), definitions); err != nil {
		log.Fatalf("sync scheduled jobs: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, cfg, runtimeAPI.Handler(profileStateAPI.Handler(conversationAPI.Handler())), conversationWorkflow, scheduler, workers); err != nil {
		log.Fatal(err)
	}
}

func parseMainOptions(arguments []string) (mainOptions, error) {
	flags := flag.NewFlagSet("job-agent", flag.ContinueOnError)
	migrateUp := flags.Bool("migrate-up", false, "apply pending database migrations before startup")
	if err := flags.Parse(arguments); err != nil {
		return mainOptions{}, err
	}
	if flags.NArg() != 1 {
		return mainOptions{}, errors.New("exactly one config path is required")
	}
	return mainOptions{configPath: flags.Arg(0), migrateUp: *migrateUp}, nil
}

func applicationPreparer(profile appconfig.Profile) (applicationoperator.ApplicationPreparer, error) {
	preparer, err := applicationoperator.NewRuleTemplatePreparer(applicationoperator.RuleTemplateConfig{
		IncludeAny:      profile.Applications.Qualification.IncludeAny,
		ExcludeAny:      profile.Applications.Qualification.ExcludeAny,
		StaticMessage:   profile.Applications.Message,
		MessageTemplate: profile.Applications.ResolvedMessageTemplate(),
	})
	if err != nil {
		return nil, err
	}
	return preparer, nil
}

func configureSearchRuns(
	ctx context.Context,
	cfg appconfig.Config,
	instances map[string]adapter.Adapter,
	profiles map[core.ProfileID]profileRuntime,
	campaignRoutes map[core.SearchID]struct{},
	store *storesqlite.Store,
) (*workflow.SearchPageHandler, int, error) {
	clock := workflow.SystemClock{}
	ids := workflow.RandomIDGenerator{}
	handler, err := workflow.NewSearchPageHandler(store, store, clock, ids)
	if err != nil {
		return nil, 0, err
	}
	configured := 0
	for _, search := range cfg.Searches {
		if _, ownedByCampaign := campaignRoutes[core.SearchID(search.Tag)]; ownedByCampaign {
			continue
		}
		instance := instances[search.Adapter]
		targetProfiles := make([]core.ProfileID, 0, len(search.Profiles))
		var searchProfileID core.ProfileID
		for _, value := range search.Profiles {
			profileID := core.ProfileID(value)
			runtime := profiles[profileID]
			if !runtime.canReadVacancies() {
				continue
			}
			targetProfiles = append(targetProfiles, profileID)
			if searchProfileID == "" {
				searchProfileID = profileID
			}
		}
		if searchProfileID == "" || len(targetProfiles) == 0 {
			log.Printf("search %q is disabled until one of its profiles is authorized", search.Tag)
			continue
		}
		searchWorkflow, err := workflow.NewSearchWorkflow(instance, store, store, store, clock, ids)
		if err != nil {
			return nil, 0, fmt.Errorf("create search workflow %q: %w", search.Tag, err)
		}
		searchID := core.SearchID(search.Tag)
		if err := handler.Register(searchID, searchWorkflow); err != nil {
			return nil, 0, err
		}
		correlation, err := ids.NewID("correlation")
		if err != nil {
			return nil, 0, err
		}
		run, err := core.NewSearchRun(
			searchID, search.Adapter, core.Platform(instance.Name()), searchProfileID,
			targetProfiles, search.Query, core.CorrelationID(correlation), clock.Now(),
		)
		if err != nil {
			return nil, 0, fmt.Errorf("build search run %q: %w", search.Tag, err)
		}
		if _, err := handler.EnsureRun(ctx, run); err != nil {
			return nil, 0, fmt.Errorf("ensure search run %q: %w", search.Tag, err)
		}
		configured++
	}
	return handler, configured, nil
}

func configureApplicationCampaigns(
	cfg appconfig.Config,
	instances map[string]adapter.Adapter,
	profiles map[core.ProfileID]profileRuntime,
	store *storesqlite.Store,
) (*workflow.ApplicationCampaignHandler, []jobscheduler.Definition, map[core.SearchID]struct{}, int, error) {
	clock := workflow.SystemClock{}
	ids := workflow.RandomIDGenerator{}
	handler, err := workflow.NewApplicationCampaignHandler(store, store, store, store, clock, ids, 5*time.Second)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	searches := make(map[string]appconfig.Search, len(cfg.Searches))
	for _, search := range cfg.Searches {
		searches[search.Tag] = search
	}
	registered := make(map[core.SearchID]struct{})
	ownedRoutes := make(map[core.SearchID]struct{})
	definitions := make([]jobscheduler.Definition, 0)
	configuredJobs := 0
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionApplicationCampaign {
			continue
		}
		runnable := true
		for _, value := range job.Action.Profiles {
			runtime := profiles[core.ProfileID(value)]
			if !runtime.canReadVacancies() {
				log.Printf("application campaign %q is disabled until profile %q has read access", job.Tag, value)
				runnable = false
			}
		}
		var platform core.Platform
		for _, value := range job.Action.Routes {
			search := searches[value]
			searchID := core.SearchID(search.Tag)
			instance := instances[search.Adapter]
			if platform == "" {
				platform = core.Platform(instance.Name())
			}
			if _, exists := registered[searchID]; exists {
				continue
			}
			var searchProfileID core.ProfileID
			for _, profile := range search.Profiles {
				profileID := core.ProfileID(profile)
				runtime := profiles[profileID]
				if runtime.canReadVacancies() {
					searchProfileID = profileID
					break
				}
			}
			if searchProfileID == "" {
				log.Printf("application campaign route %q is disabled until one of its profiles has read access", search.Tag)
				runnable = false
				continue
			}
			if err := handler.Register(workflow.ApplicationCampaignRoute{
				SearchID: searchID, Platform: core.Platform(instance.Name()), SearchProfileID: searchProfileID,
				Query: search.Query, Searcher: instance,
			}); err != nil {
				return nil, nil, nil, configuredJobs, err
			}
			registered[searchID] = struct{}{}
		}
		if !runnable {
			continue
		}
		profileIDs := make([]core.ProfileID, 0, len(job.Action.Profiles))
		for _, value := range job.Action.Profiles {
			profileIDs = append(profileIDs, core.ProfileID(value))
		}
		routeIDs := make([]core.SearchID, 0, len(job.Action.Routes))
		for _, value := range job.Action.Routes {
			routeID := core.SearchID(value)
			routeIDs = append(routeIDs, routeID)
			ownedRoutes[routeID] = struct{}{}
		}
		payload, err := json.Marshal(core.NewApplicationCampaignStartPayload(
			job.Tag, profileIDs, routeIDs, job.Action.TargetSuccessful, job.Action.MaxInFlight,
		))
		if err != nil {
			return nil, nil, nil, configuredJobs, fmt.Errorf("encode application campaign %q: %w", job.Tag, err)
		}
		for index, trigger := range job.Triggers {
			minimum, maximum := trigger.Jitter.Durations()
			definitions = append(definitions, jobscheduler.Definition{
				JobTag: job.Tag, TriggerIndex: index, Expression: trigger.Expression, Timezone: trigger.Timezone,
				ActionType: core.TaskApplicationCampaign, Platform: platform, ProfileID: profileIDs[0],
				Payload: payload, JitterMin: minimum, JitterMax: maximum,
			})
		}
		configuredJobs++
	}
	return handler, definitions, ownedRoutes, configuredJobs, nil
}

type profileRuntime struct {
	Status            core.ProfileStatus
	ExternalAccountID string
	Reader            adapter.ProfileReader
	BrowserReader     adapter.VacancyReader
}

func (runtime profileRuntime) canReadVacancies() bool {
	return runtime.Status == core.ProfileEnabled && (runtime.Reader != nil || runtime.BrowserReader != nil)
}

func probeProfileAuthorizations(ctx context.Context, configured []appconfig.Profile, instances map[string]adapter.Adapter) (map[core.ProfileID]profileRuntime, error) {
	profiles := make(map[core.ProfileID]profileRuntime, len(configured))
	for _, profile := range configured {
		profileID := core.ProfileID(profile.Tag)
		if !profile.Enabled {
			profiles[profileID] = profileRuntime{Status: core.ProfileDisabled}
			continue
		}
		profiles[profileID] = profileRuntime{Status: core.ProfileEnabled}
		if profile.CredentialsRef == "" {
			continue
		}
		instance := instances[profile.Adapter]
		factory, ok := instance.(adapter.ProfileReaderFactory)
		if !ok {
			return nil, fmt.Errorf("adapter %q does not support authenticated profile reads", profile.Adapter)
		}
		reader, err := factory.NewProfileReader(profileID, profile.CredentialsRef)
		if err != nil {
			return nil, fmt.Errorf("create profile reader %q: %w", profile.Tag, err)
		}
		probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		identity, err := reader.ReadProfile(probeCtx, profileID)
		cancel()
		if core.ErrorIsCategory(err, core.ErrorUnauthorized) {
			profiles[profileID] = profileRuntime{Status: core.ProfileAuthRequired, Reader: reader}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read profile %q: %w", profile.Tag, err)
		}
		profiles[profileID] = profileRuntime{Status: core.ProfileEnabled, ExternalAccountID: identity.ExternalAccountID, Reader: reader}
	}
	return profiles, nil
}

func serve(ctx context.Context, cfg appconfig.Config, handler http.Handler, conversationWorkflow *workflow.ConversationWorkflow, scheduler *jobscheduler.Scheduler, workers []*taskworker.Worker) error {
	if _, err := conversationWorkflow.ReconcileDueFollowUps(ctx, time.Now().UTC()); err != nil {
		return fmt.Errorf("initial follow-up reconcile: %w", err)
	}
	go reconcileFollowUps(ctx, cfg.Server.ReconcileInterval(), conversationWorkflow)
	if _, err := scheduler.ReconcileDue(ctx); err != nil {
		return fmt.Errorf("initial job reconcile: %w", err)
	}
	go reconcileJobs(ctx, cfg.Server.SchedulerInterval(), scheduler)

	server := &http.Server{
		Addr:              cfg.Server.ListenAddress(),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	result := make(chan error, 1)
	workerErrors := make(chan error, len(workers))
	for _, instance := range workers {
		go func(instance *taskworker.Worker) {
			workerErrors <- instance.Run(ctx)
		}(instance)
	}
	go func() {
		log.Printf("job-agent API listening on http://%s", server.Addr)
		result <- server.ListenAndServe()
	}()

	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve conversation API: %w", err)
	case err := <-workerErrors:
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("task worker: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown conversation API: %w", err)
		}
		return nil
	}
}

func resumeTouchDefinitions(cfg appconfig.Config, instances map[string]adapter.Adapter, touchers *taskworker.ResumeToucherRegistry) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionResumeTouch {
			continue
		}
		profile := profiles[job.Action.Profile]
		profileID := core.ProfileID(profile.Tag)
		if !profile.Enabled || !touchers.Has(profileID) {
			continue
		}
		resumeID := job.Action.Resume
		if resumeID == "" {
			resumeID = profile.Resume
		}
		payload, err := json.Marshal(core.ResumeTouchPayload{ProfileID: profileID, ResumeID: resumeID})
		if err != nil {
			return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
		}
		instance := instances[profile.Adapter]
		for index, trigger := range job.Triggers {
			minimum, maximum := trigger.Jitter.Durations()
			definitions = append(definitions, jobscheduler.Definition{
				JobTag: job.Tag, TriggerIndex: index, Expression: trigger.Expression, Timezone: trigger.Timezone,
				ActionType: core.TaskResumeTouch, Platform: core.Platform(instance.Name()), ProfileID: profileID,
				Payload: payload, JitterMin: minimum, JitterMax: maximum,
			})
		}
	}
	return definitions, nil
}

func conversationWorkers(consumer broker.TaskConsumer, handlers *taskworker.ConversationHandlers) ([]*taskworker.Worker, error) {
	definitions := []struct {
		taskType core.TaskType
		handler  taskworker.HandlerFunc
	}{
		{core.TaskConversationSend, handlers.Send},
		{core.TaskConversationFollowUp, handlers.FollowUp},
		{core.TaskConversationMarkRead, handlers.MarkRead},
		{core.TaskConversationSync, handlers.Sync},
	}
	result := make([]*taskworker.Worker, 0, len(definitions))
	for _, definition := range definitions {
		instance, err := newTaskWorker(consumer, definition.taskType, definition.handler)
		if err != nil {
			return nil, err
		}
		result = append(result, instance)
	}
	return result, nil
}

func newTaskWorker(consumer broker.TaskConsumer, taskType core.TaskType, handler taskworker.HandlerFunc) (*taskworker.Worker, error) {
	return taskworker.New(consumer, handler, taskworker.SystemClock{}, taskworker.Config{
		ID: "worker-" + string(taskType), TaskType: taskType,
		LeaseDuration: 2 * time.Minute, HeartbeatInterval: 30 * time.Second,
		PollInterval: time.Second, RetryBaseDelay: 5 * time.Second,
		BlockedRetryDelay: 5 * time.Minute, MaxAttempts: 5,
	})
}

func reconcileFollowUps(ctx context.Context, interval time.Duration, conversationWorkflow *workflow.ConversationWorkflow) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			result, err := conversationWorkflow.ReconcileDueFollowUps(ctx, now.UTC())
			if err != nil {
				log.Printf("reconcile follow-ups: %v", err)
				continue
			}
			if result.TasksCreated > 0 {
				log.Printf("reconciled %d due follow-ups, created %d tasks", result.Due, result.TasksCreated)
			}
		}
	}
}

func reconcileJobs(ctx context.Context, interval time.Duration, scheduler *jobscheduler.Scheduler) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := scheduler.ReconcileDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("reconcile scheduled jobs: %v", err)
			}
		}
	}
}
