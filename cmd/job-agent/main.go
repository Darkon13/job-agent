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
	"strings"
	"syscall"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/adapters/hh"
	"github.com/Darkon13/job-agent/api/httpapi"
	"github.com/Darkon13/job-agent/auth"
	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/browser"
	"github.com/Darkon13/job-agent/buildinfo"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	applicationoperator "github.com/Darkon13/job-agent/operator"
	"github.com/Darkon13/job-agent/operator/openairesponses"
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
	if buildinfo.Requested(os.Args[1:]) {
		if err := buildinfo.Write("job-agent", os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) >= 3 && os.Args[1] == "profile" && os.Args[2] == "bootstrap" {
		if err := runProfileBootstrap(context.Background(), os.Args[3:], os.Stdout, nil); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "startup" {
		if err := runProfileStartup(context.Background(), os.Args[2:], os.Stdout, nil); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "auth" {
		if err := runAuth(context.Background(), os.Args[2:], os.Stdout, nil); err != nil {
			log.Fatal(err)
		}
		return
	}
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
	if err := applyServerEnvironment(&cfg, os.LookupEnv); err != nil {
		log.Fatalf("apply server environment: %v", err)
	}
	employerMatcher, err := cfg.BuildEmployerGroupMatcher()
	if err != nil {
		log.Fatalf("build employer groups: %v", err)
	}
	applicationModels, err := buildApplicationModels(cfg.Models, os.LookupEnv)
	if err != nil {
		log.Fatalf("build application models: %v", err)
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
	vacancyTestWorkflow, err := workflow.NewVacancyTestWorkflow(store, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	if err != nil {
		log.Fatalf("create vacancy test workflow: %v", err)
	}
	conversationAPI, err := httpapi.NewConversationAPI(store, conversationWorkflow)
	if err != nil {
		log.Fatalf("create conversation API: %v", err)
	}
	runtimeAPI, err := httpapi.NewRuntimeAPI(store)
	if err != nil {
		log.Fatalf("create runtime API: %v", err)
	}
	taskControlWorkflow, err := workflow.NewTaskControlWorkflow(store, workflow.SystemClock{})
	if err != nil {
		log.Fatalf("create task control workflow: %v", err)
	}
	taskAPI, err := httpapi.NewTaskAPI(store, taskControlWorkflow)
	if err != nil {
		log.Fatalf("create task API: %v", err)
	}
	applicationRemovalWorkflow, err := workflow.NewApplicationRemovalWorkflow(
		store, store, workflow.SystemClock{}, workflow.RandomIDGenerator{},
	)
	if err != nil {
		log.Fatalf("create application removal workflow: %v", err)
	}
	applicationAPI, err := httpapi.NewApplicationAPI(applicationRemovalWorkflow)
	if err != nil {
		log.Fatalf("create application API: %v", err)
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
	profileStateWriters := taskworker.NewProfileStateWriterRegistry()
	profileStatePlatforms := make(map[core.ProfileID]core.Platform)
	conversationTransports := taskworker.NewConversationTransportRegistry()
	applicationTransports := taskworker.NewApplicationTransportRegistry()
	applicationStateObservers := taskworker.NewApplicationStateObserverRegistry()
	resumeTouchers := taskworker.NewResumeToucherRegistry()
	resumePublishers := taskworker.NewResumePublisherRegistry()
	testCapturers := taskworker.NewVacancyTestCapturerRegistry()
	testSubmitters := taskworker.NewVacancyTestSubmitterRegistry()
	activityObservers := taskworker.NewProfileActivityObserverRegistry()
	applicationPlans := make(taskworker.StaticApplicationPlans)
	applicationTailoringPlans := make(map[core.ProfileID]taskworker.ApplicationTailoringPlan)
	for _, profile := range cfg.Profiles {
		preparer, err := applicationPreparer(profile, employerMatcher, applicationModels)
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
		browserConversationsReady := false
		var browserProfileStateWriter adapter.ProfileStateWriter
		if profile.StateFile != "" {
			if binder, ok := instance.(adapter.BrowserSessionBinder); ok {
				reader, err := binder.BindBrowserSession(profileID, profile.StateFile)
				if err != nil {
					log.Fatalf("bind browser session for profile %q: %v", profile.Tag, err)
				}
				runtime.BrowserReader = reader
				profiles[profileID] = runtime
				log.Printf("profile %q has a browser-backed read session", profile.Tag)
				if capturer, ok := instance.(adapter.VacancyTestCapturer); ok {
					if err := testCapturers.Register(profileID, capturer); err != nil {
						log.Fatalf("register vacancy test capturer for profile %q: %v", profile.Tag, err)
					}
					log.Printf("profile %q can capture vacancy tests through the browser session", profile.Tag)
				}
				if profileStateReader, ok := instance.(adapter.ProfileStateReader); ok {
					profileStateReaders[profileID] = profileStateReader
				}
			}
			if binder, ok := instance.(adapter.BrowserProfileStateSessionBinder); ok {
				writer, err := binder.BindBrowserProfileStateSession(profileID, profile.StateFile)
				if err != nil {
					log.Fatalf("bind browser profile state session for profile %q: %v", profile.Tag, err)
				}
				browserProfileStateWriter = writer
			}
			if binder, ok := instance.(adapter.BrowserConversationSessionBinder); ok {
				transport, err := binder.BindBrowserConversationSession(profileID, profile.StateFile, adapter.BrowserConversationOptions{
					AllowSend: profile.Conversations.AllowSend, AllowMarkRead: profile.Conversations.AllowMarkRead,
				})
				if err != nil {
					log.Fatalf("bind browser conversation session for profile %q: %v", profile.Tag, err)
				}
				if err := conversationTransports.Register(profileID, transport); err != nil {
					log.Fatalf("register browser conversation transport for profile %q: %v", profile.Tag, err)
				}
				browserConversationsReady = true
				log.Printf("profile %q has a browser-backed conversation session", profile.Tag)
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
					if submitter, ok := instance.(adapter.VacancyTestSubmitter); ok {
						if err := testSubmitters.Register(profileID, submitter); err != nil {
							log.Fatalf("register vacancy test submitter for profile %q: %v", profile.Tag, err)
						}
					}
					browserApplicationsReady = true
					log.Printf("profile %q uses explicit browser-backed application transport", profile.Tag)
				}
			}
		}
		if apiReady || runtime.BrowserReader != nil {
			if profileStateReader, ok := instance.(adapter.ProfileStateReader); ok {
				profileStateReaders[profileID] = profileStateReader
			}
			writer, ok := instance.(adapter.ProfileStateWriter)
			if !ok {
				writer = browserProfileStateWriter
			}
			if writer != nil {
				if err := profileStateWriters.Register(profileID, writer); err != nil {
					log.Fatalf("register profile state writer for profile %q: %v", profile.Tag, err)
				}
				profileStatePlatforms[profileID] = core.Platform(instance.Name())
			}
		}
		if apiReady {
			if transport, ok := instance.(adapter.ConversationTransport); ok && !browserConversationsReady {
				if err := conversationTransports.Register(profileID, transport); err != nil {
					log.Fatalf("register conversation transport for profile %q: %v", profile.Tag, err)
				}
			}
			if transport, ok := instance.(adapter.ApplicationTransport); ok {
				if err := applicationTransports.Register(profileID, transport); err != nil {
					log.Fatalf("register application transport for profile %q: %v", profile.Tag, err)
				}
			}
			if observer, ok := instance.(adapter.ApplicationStateObserver); ok {
				if err := applicationStateObservers.Register(profileID, observer); err != nil {
					log.Fatalf("register application state observer for profile %q: %v", profile.Tag, err)
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
			jitterMin, jitterMax, err := profile.Applications.SubmitJitterDurations(instance.Name())
			if err != nil {
				log.Fatalf("resolve application pacing for profile %q: %v", profile.Tag, err)
			}
			applicationPlan := taskworker.ApplicationPlan{
				ResumeID: profile.Resume, Mode: core.ApplicationExecutionMode(profile.Applications.ExecutionMode()),
				Message: profile.Applications.Message, Preparer: preparer,
				DailyLimit:      profile.Applications.EffectiveDailyLimit(instance.Name()),
				SubmitJitterMin: jitterMin, SubmitJitterMax: jitterMax,
				Timezone: profile.Applications.LocationName(),
			}
			tailoringProcessor, tailoringErr := applicationTailoringProcessor(profile, applicationModels)
			if tailoringErr != nil {
				log.Fatalf("build application tailoring processor for profile %q: %v", profile.Tag, tailoringErr)
			}
			if tailoringProcessor != nil {
				if _, hasReader := profileStateReaders[profileID]; !hasReader {
					log.Fatalf("profile %q application tailoring requires a profile state reader", profile.Tag)
				}
				if _, resolveErr := profileStateWriters.Resolve(profileID); resolveErr != nil {
					log.Fatalf("profile %q application tailoring requires a profile state writer", profile.Tag)
				}
				tailoringPlan := taskworker.ApplicationTailoringPlan{
					Processor:       tailoringProcessor,
					AllowedPaths:    []string{applicationoperator.ResumeSkillsPath(profile.Resume)},
					EmployerMatcher: employerMatcher,
				}
				applicationTailoringPlans[profileID] = tailoringPlan
				applicationPlan.Tailoring = &tailoringPlan
			}
			applicationPlans[profileID] = applicationPlan
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
		if observer, ok := instance.(adapter.ProfileActivityObserver); ok {
			if err := activityObservers.Register(profileID, observer); err != nil {
				log.Fatalf("register profile activity observer for profile %q: %v", profile.Tag, err)
			}
		} else if instance.Name() == hh.Name && profile.StateFile != "" {
			if _, err := os.Stat(profile.StateFile); err == nil {
				observer, err := hh.NewResumeTouchTransport(profile.StateFile, nil)
				if err != nil {
					log.Fatalf("create HH profile activity observer for profile %q: %v", profile.Tag, err)
				}
				if err := activityObservers.Register(profileID, observer); err != nil {
					log.Fatalf("register HH profile activity observer for profile %q: %v", profile.Tag, err)
				}
			}
		}
		if apiReady {
			if publisher, ok := instance.(adapter.ResumePublisher); ok {
				if err := resumePublishers.Register(profileID, publisher); err != nil {
					log.Fatalf("register resume publisher for profile %q: %v", profile.Tag, err)
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
	profileBootstrapWorkflow, err := workflow.NewProfileBootstrapWorkflow(profileStatePlanner, profileStateApplyWorkflow)
	if err != nil {
		log.Fatalf("create profile bootstrap workflow: %v", err)
	}
	if err := reconcileConfiguredProfileBootstraps(
		context.Background(), cfg, profiles, profileStateReaders, profileBootstrapWorkflow,
	); err != nil {
		log.Fatalf("reconcile profile bootstrap: %v", err)
	}
	profileStateReconcileWorkflow, err := workflow.NewProfileStateReconcileWorkflow(
		profileStatePlanner, store, workflow.SystemClock{}, workflow.RandomIDGenerator{}, profileStatePlatforms,
	)
	if err != nil {
		log.Fatalf("create profile state reconcile workflow: %v", err)
	}
	profileStateAPI, err := httpapi.NewProfileStateAPI(
		profileStatePlanner, profileStateApplyWorkflow, profileStateReconcileWorkflow, store, profileStateReaders,
	)
	if err != nil {
		log.Fatalf("create profile state API: %v", err)
	}
	conversationHandlers, err := taskworker.NewConversationHandlers(
		store, store, conversationWorkflow, conversationTransports, taskworker.StaticMessageResolver{}, taskworker.SystemClock{},
	)
	if err != nil {
		log.Fatalf("create conversation handlers: %v", err)
	}
	profileMutationLane := taskworker.NewProfileMutationLane()
	workers, err := conversationWorkers(store, conversationHandlers, profileMutationLane)
	if err != nil {
		log.Fatalf("create conversation workers: %v", err)
	}
	followUpSelectionHandler, err := taskworker.NewConversationFollowUpSelectionHandler(conversationWorkflow)
	if err != nil {
		log.Fatalf("create conversation follow-up selection handler: %v", err)
	}
	followUpSelectionWorker, err := newTaskWorker(store, core.TaskConversationFollowUpSelect, followUpSelectionHandler.Handle)
	if err != nil {
		log.Fatalf("create conversation follow-up selection worker: %v", err)
	}
	workers = append(workers, followUpSelectionWorker)
	if len(profileStateReaders) > 0 && profileStateWriters.Count() > 0 {
		profileStateReconcileHandler, err := workflow.NewProfileStateReconcileHandler(
			profileStatePlanner, profileStateApplyWorkflow, profileStateReaders,
		)
		if err != nil {
			log.Fatalf("create profile state reconcile handler: %v", err)
		}
		profileStateReconcileWorker, err := newTaskWorker(
			store, core.TaskProfileStateReconcile, profileStateReconcileHandler.Handle,
		)
		if err != nil {
			log.Fatalf("create profile state reconcile worker: %v", err)
		}
		workers = append(workers, profileStateReconcileWorker)
	}
	if profileStateWriters.Count() > 0 {
		profileStateHandler, err := taskworker.NewProfileStateApplyHandler(store, profileStateWriters)
		if err != nil {
			log.Fatalf("create profile state apply handler: %v", err)
		}
		profileStateWorker, err := newTaskWorker(
			store, core.TaskProfileStateApply, profileMutationLane.Wrap(profileStateHandler.Handle),
		)
		if err != nil {
			log.Fatalf("create profile state apply worker: %v", err)
		}
		workers = append(workers, profileStateWorker)
	}
	var applicationTailoringCoordinator *taskworker.ApplicationTailoringCoordinator
	if len(applicationTailoringPlans) > 0 {
		applicationTailoringCoordinator, err = taskworker.NewApplicationTailoringCoordinator(
			store, store, profileStateReaders, profileStateWriters, taskworker.SystemClock{}, workflow.RandomIDGenerator{},
		)
		if err != nil {
			log.Fatalf("create application tailoring coordinator: %v", err)
		}
	}
	if len(applicationPlans) > 0 && applicationTransports.VacancyReaderCount() > 0 {
		applicationHandler, err := taskworker.NewApplicationHandler(
			store, store, store, store, store, applicationTransports, applicationPlans,
			taskworker.UniformApplicationJitter{}, taskworker.SystemClock{},
		)
		if err != nil {
			log.Fatalf("create application handler: %v", err)
		}
		applicationHandler.ConfigureTailoring(applicationTailoringCoordinator)
		applicationHandler.ConfigureTestChain(store, vacancyTestWorkflow)
		applicationWorker, err := newTaskWorkerBlockedBy(
			store, core.TaskApplicationSubmit, core.TaskProfileStateApply,
			profileMutationLane.Wrap(applicationHandler.Handle),
		)
		if err != nil {
			log.Fatalf("create application worker: %v", err)
		}
		workers = append(workers, applicationWorker)
	}
	applicationRemovalHandler, err := taskworker.NewApplicationRemovalHandler(
		store, store, store, applicationStateObservers, taskworker.SystemClock{},
	)
	if err != nil {
		log.Fatalf("create application removal handler: %v", err)
	}
	applicationRemovalWorker, err := newTaskWorker(
		store, core.TaskApplicationRemove, profileMutationLane.Wrap(applicationRemovalHandler.Handle),
	)
	if err != nil {
		log.Fatalf("create application removal worker: %v", err)
	}
	workers = append(workers, applicationRemovalWorker)
	applicationRetentionHandler, err := taskworker.NewApplicationRetentionHandler(
		store, applicationStateObservers, applicationRemovalWorkflow, taskworker.SystemClock{},
	)
	if err != nil {
		log.Fatalf("create application retention handler: %v", err)
	}
	applicationRetentionWorker, err := newTaskWorker(
		store, core.TaskApplicationRetention, applicationRetentionHandler.Handle,
	)
	if err != nil {
		log.Fatalf("create application retention worker: %v", err)
	}
	workers = append(workers, applicationRetentionWorker)
	if resumeTouchers.Count() > 0 {
		resumeHandler, err := taskworker.NewResumeTouchHandler(resumeTouchers, store, taskworker.SystemClock{})
		if err != nil {
			log.Fatalf("create resume touch handler: %v", err)
		}
		resumeWorker, err := newTaskWorker(
			store, core.TaskResumeTouch, profileMutationLane.Wrap(resumeHandler.Handle),
		)
		if err != nil {
			log.Fatalf("create resume touch worker: %v", err)
		}
		workers = append(workers, resumeWorker)
	}
	if testCapturers.Count() > 0 {
		testCaptureHandler, err := taskworker.NewVacancyTestCaptureHandler(testCapturers, store, taskworker.SystemClock{})
		if err != nil {
			log.Fatalf("create vacancy test capture handler: %v", err)
		}
		testCaptureWorker, err := newTaskWorker(store, core.TaskTestCapture, testCaptureHandler.Handle)
		if err != nil {
			log.Fatalf("create vacancy test capture worker: %v", err)
		}
		workers = append(workers, testCaptureWorker)
	}
	if testSubmitters.Count() > 0 {
		questionnaireAnswerHandler, err := taskworker.NewQuestionnaireAnswerHandler(testSubmitters)
		if err != nil {
			log.Fatalf("create questionnaire answer handler: %v", err)
		}
		questionnaireAnswerWorker, err := newTaskWorker(
			store, core.TaskQuestionnaireAnswer, profileMutationLane.Wrap(questionnaireAnswerHandler.Handle),
		)
		if err != nil {
			log.Fatalf("create questionnaire answer worker: %v", err)
		}
		workers = append(workers, questionnaireAnswerWorker)
	}
	testCompleteHandler, err := taskworker.NewTestCompleteHandler(store, taskworker.SystemClock{})
	if err != nil {
		log.Fatalf("create test complete handler: %v", err)
	}
	testCompleteWorker, err := newTaskWorker(store, core.TaskTestComplete, testCompleteHandler.Handle)
	if err != nil {
		log.Fatalf("create test complete worker: %v", err)
	}
	workers = append(workers, testCompleteWorker)
	if resumePublishers.Count() > 0 {
		resumePublishHandler, err := taskworker.NewResumePublishHandler(resumePublishers)
		if err != nil {
			log.Fatalf("create resume publish handler: %v", err)
		}
		resumePublishWorker, err := newTaskWorker(
			store, core.TaskResumePublish, profileMutationLane.Wrap(resumePublishHandler.Handle),
		)
		if err != nil {
			log.Fatalf("create resume publish worker: %v", err)
		}
		workers = append(workers, resumePublishWorker)
	}
	if activityObservers.Count() > 0 {
		activityHandler, err := taskworker.NewProfileActivityObserveHandler(store, activityObservers)
		if err != nil {
			log.Fatalf("create profile activity observation handler: %v", err)
		}
		activityWorker, err := newTaskWorker(store, core.TaskProfileActivityObserve, activityHandler.Handle)
		if err != nil {
			log.Fatalf("create profile activity observation worker: %v", err)
		}
		workers = append(workers, activityWorker)
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
	resumePublishDefinitions, err := resumePublishDefinitions(cfg, instances, resumePublishers)
	if err != nil {
		log.Fatalf("build resume publish scheduled jobs: %v", err)
	}
	definitions = append(definitions, resumePublishDefinitions...)
	activityDefinitions, err := profileActivityDefinitions(cfg, instances, activityObservers)
	if err != nil {
		log.Fatalf("build profile activity scheduled jobs: %v", err)
	}
	definitions = append(definitions, activityDefinitions...)
	conversationDefinitions, err := conversationDiscoveryDefinitions(cfg, instances, conversationTransports)
	if err != nil {
		log.Fatalf("build conversation sync jobs: %v", err)
	}
	definitions = append(definitions, conversationDefinitions...)
	followUpSelectionDefinitions, err := conversationFollowUpSelectionDefinitions(cfg, instances, conversationTransports)
	if err != nil {
		log.Fatalf("build conversation follow-up selection jobs: %v", err)
	}
	definitions = append(definitions, followUpSelectionDefinitions...)
	retentionDefinitions, err := applicationRetentionDefinitions(cfg, instances, applicationStateObservers)
	if err != nil {
		log.Fatalf("build application retention scheduled jobs: %v", err)
	}
	definitions = append(definitions, retentionDefinitions...)
	profileStateDefinitions, err := profileStateReconcileDefinitions(
		cfg, profileStateResources, profileStateReaders, profileStatePlatforms,
	)
	if err != nil {
		log.Fatalf("build profile state scheduled jobs: %v", err)
	}
	definitions = append(definitions, profileStateDefinitions...)
	definitions = append(definitions, campaignDefinitions...)
	scheduler, err := jobscheduler.New(store, store, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	if err != nil {
		log.Fatalf("create scheduler: %v", err)
	}
	if err := scheduler.Sync(context.Background(), definitions); err != nil {
		log.Fatalf("sync scheduled jobs: %v", err)
	}
	jobRunWorkflow, err := workflow.NewJobRunWorkflow(
		store, workflow.SystemClock{}, workflow.RandomIDGenerator{}, jobRunDefinitions(definitions),
	)
	if err != nil {
		log.Fatalf("create job run workflow: %v", err)
	}
	jobAPI, err := httpapi.NewJobAPI(jobRunWorkflow)
	if err != nil {
		log.Fatalf("create job API: %v", err)
	}
	authAPI, err := configureAuthAPI(cfg, instances, store)
	if err != nil {
		log.Fatalf("configure auth API: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var handler http.Handler = runtimeAPI.Handler(jobAPI.Handler(taskAPI.Handler(profileStateAPI.Handler(applicationAPI.Handler(conversationAPI.Handler())))))
	if authAPI != nil {
		handler = authAPI.Handler(handler)
	}
	if err := serve(ctx, cfg, handler, conversationWorkflow, scheduler, workers); err != nil {
		log.Fatal(err)
	}
}

// configureAuthAPI wires the interactive login control plane when the browser
// worker is configured. The credential writer runs with Force enabled because
// the CLI enforces the explicit --force decision before creating a session.
func configureAuthAPI(cfg appconfig.Config, instances map[string]adapter.Adapter, store *storesqlite.Store) (*httpapi.AuthAPI, error) {
	baseURL := strings.TrimSpace(os.Getenv("BROWSER_WORKER_URL"))
	token := strings.TrimSpace(os.Getenv("BROWSER_WORKER_TOKEN"))
	if baseURL == "" || token == "" {
		log.Printf("browser worker is not configured; interactive auth API is disabled")
		return nil, nil
	}
	client, err := browser.NewHTTPClient(browser.HTTPConfig{BaseURL: baseURL, Token: token})
	if err != nil {
		return nil, err
	}
	settings := make([]hh.LoginSettings, 0, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		if !profile.Enabled || strings.TrimSpace(profile.StateFile) == "" {
			continue
		}
		instance := instances[profile.Adapter]
		if instance == nil || instance.Name() != hh.Name {
			continue
		}
		settings = append(settings, hh.LoginSettings{
			ProfileID: core.ProfileID(profile.Tag), StateFile: profile.StateFile,
		})
	}
	if len(settings) == 0 {
		log.Printf("browser worker is configured but no HH profile has a state file; interactive auth API is disabled")
		return nil, nil
	}
	driver, err := hh.NewLoginDriver(client, settings)
	if err != nil {
		return nil, err
	}
	service, err := auth.NewService(
		store, auth.NewMemoryChallengeStore(), driver,
		&auth.FileCredentialWriter{Force: true}, &auth.FileBrowserStateWriter{},
		workflow.SystemClock{}, workflow.RandomIDGenerator{},
	)
	if err != nil {
		return nil, err
	}
	return httpapi.NewAuthAPI(service)
}

func jobRunDefinitions(definitions []jobscheduler.Definition) []workflow.JobRunDefinition {
	result := make([]workflow.JobRunDefinition, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, workflow.JobRunDefinition{
			Tag: definition.JobTag, TaskType: definition.ActionType, Platform: definition.Platform,
			ProfileID: definition.ProfileID, Payload: definition.Payload, Priority: definition.Priority,
		})
	}
	return result
}

func reconcileConfiguredProfileBootstraps(
	ctx context.Context,
	cfg appconfig.Config,
	profiles map[core.ProfileID]profileRuntime,
	readers map[core.ProfileID]adapter.ProfileStateReader,
	bootstrap *workflow.ProfileBootstrapWorkflow,
) error {
	for _, profile := range cfg.Profiles {
		if profile.Bootstrap == nil {
			continue
		}
		profileID := core.ProfileID(profile.Tag)
		if !profile.Enabled {
			log.Printf("profile bootstrap for %q is disabled with the profile", profile.Tag)
			continue
		}
		if profiles[profileID].Status == core.ProfileAuthRequired {
			log.Printf("profile bootstrap for %q is waiting for authentication", profile.Tag)
			continue
		}
		resource, resolved := profile.Bootstrap.ResolvedResource()
		if !resolved {
			return fmt.Errorf("profile %q bootstrap source was not resolved by config loader", profile.Tag)
		}
		reader := readers[profileID]
		if reader == nil {
			return fmt.Errorf("profile %q bootstrap requires an authorized profile state reader", profile.Tag)
		}
		bootstrapCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result, err := bootstrap.RunWhenEmpty(
			bootstrapCtx, resource, reader, "config:profile-bootstrap:"+resource.Tag,
		)
		cancel()
		if err != nil {
			return fmt.Errorf("profile %q: %w", profile.Tag, err)
		}
		switch {
		case !result.ConditionMatched:
			log.Printf("profile bootstrap for %q skipped: at least one declared field is already populated", profile.Tag)
		case result.Proposal.Status == core.ProfileStateProposalNoChanges:
			log.Printf("profile bootstrap for %q has no changes", profile.Tag)
		case result.TaskCreated:
			log.Printf("profile bootstrap for %q queued apply task %q", profile.Tag, result.Task.ID)
		default:
			log.Printf("profile bootstrap for %q reused apply task %q in status %q", profile.Tag, result.Task.ID, result.Task.Status)
		}
	}
	return nil
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

func applyServerEnvironment(cfg *appconfig.Config, lookupEnv func(string) (string, bool)) error {
	if cfg == nil {
		return errors.New("server environment requires config")
	}
	if lookupEnv == nil {
		return errors.New("server environment requires lookup function")
	}
	overridden := false
	for _, item := range []struct {
		name   string
		target *string
	}{
		{name: "JOB_AGENT_SERVER_LISTEN", target: &cfg.Server.Listen},
		{name: "JOB_AGENT_SERVER_EXPOSURE", target: &cfg.Server.Exposure},
	} {
		value, exists := lookupEnv(item.name)
		if !exists {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s must not be empty when set", item.name)
		}
		*item.target = value
		overridden = true
	}
	if !overridden {
		return nil
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate overridden config: %w", err)
	}
	return nil
}

func applicationPreparer(profile appconfig.Profile, employerMatcher *applicationoperator.EmployerGroupMatcher, models map[string]applicationoperator.ApplicationMessageModel) (applicationoperator.ApplicationPreparer, error) {
	var messagePool *applicationoperator.MessagePoolConfig
	var resumeContext *applicationoperator.ApplicationResumeContext
	if facts, exists := profile.ResolvedResumeFacts(); exists {
		resumeContext = &applicationoperator.ApplicationResumeContext{
			ResumeID: facts.ResumeID, FactsTag: facts.Tag, Digest: facts.Digest, Facts: facts.Facts,
		}
	}
	messageTemplate := profile.Applications.ResolvedMessageTemplate()
	if configured, exists := profile.Applications.ResolvedMessagePool(); exists {
		messageTemplate = ""
		messagePool = applicationMessagePool(configured)
	}
	model, err := applicationModel(profile.Applications.Model, models)
	if err != nil {
		return nil, err
	}
	employerRules := make([]applicationoperator.EmployerRuleConfig, 0, len(profile.Applications.EmployerRules))
	for _, configured := range profile.Applications.ResolvedEmployerRules() {
		rule := applicationoperator.EmployerRuleConfig{
			EmployerGroups: configured.EmployerGroups,
			Action:         configured.Action,
		}
		if configured.MessagePool != nil {
			rule.MessagePool = applicationMessagePool(*configured.MessagePool)
		}
		rule.Model, err = applicationModel(configured.Model, models)
		if err != nil {
			return nil, err
		}
		employerRules = append(employerRules, rule)
	}
	preparer, err := applicationoperator.NewRuleTemplatePreparer(applicationoperator.RuleTemplateConfig{
		IncludeAny:      profile.Applications.Qualification.IncludeAny,
		ExcludeAny:      profile.Applications.Qualification.ExcludeAny,
		StaticMessage:   profile.Applications.Message,
		MessageTemplate: messageTemplate,
		MessagePool:     messagePool,
		Model:           model,
		Resume:          resumeContext,
		EmployerMatcher: employerMatcher,
		EmployerRules:   employerRules,
	})
	if err != nil {
		return nil, err
	}
	return preparer, nil
}

func buildApplicationModels(configs []appconfig.ModelProviderConfig, lookupEnv func(string) (string, bool)) (map[string]applicationoperator.ApplicationMessageModel, error) {
	if lookupEnv == nil {
		return nil, errors.New("application model environment lookup is nil")
	}
	result := make(map[string]applicationoperator.ApplicationMessageModel, len(configs))
	for _, configured := range configs {
		environment := configured.APIKeyEnvironment()
		apiKey, exists := lookupEnv(environment)
		if !exists || strings.TrimSpace(apiKey) == "" {
			return nil, fmt.Errorf("model provider %q requires non-empty environment variable %s", configured.Tag, environment)
		}
		var model applicationoperator.ApplicationMessageModel
		var err error
		switch configured.Type {
		case appconfig.ModelProviderOpenAIResponses:
			model, err = openairesponses.New(openairesponses.Config{
				BaseURL: configured.BaseURL, APIKey: apiKey, Model: configured.Model,
				MaxOutputTokens: configured.MaxOutputTokens,
			})
		default:
			err = fmt.Errorf("unsupported model provider type %q", configured.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("model provider %q: %w", configured.Tag, err)
		}
		result[strings.TrimSpace(configured.Tag)] = model
	}
	return result, nil
}

func applicationModel(configured *appconfig.ApplicationModelPolicy, models map[string]applicationoperator.ApplicationMessageModel) (*applicationoperator.ApplicationModelConfig, error) {
	if configured == nil {
		return nil, nil
	}
	provider := strings.TrimSpace(configured.Provider)
	generator := models[provider]
	if generator == nil {
		return nil, fmt.Errorf("application model references unavailable provider %q", provider)
	}
	timeout, err := time.ParseDuration(configured.Timeout)
	if err != nil || timeout <= 0 {
		return nil, fmt.Errorf("application model provider %q has invalid timeout %q", provider, configured.Timeout)
	}
	return &applicationoperator.ApplicationModelConfig{
		Tag: provider, PromptVersion: configured.PromptVersion,
		Instruction: configured.Instruction, Timeout: timeout, Generator: generator,
	}, nil
}

func applicationMessagePool(configured appconfig.ApplicationMessagePool) *applicationoperator.MessagePoolConfig {
	pool := &applicationoperator.MessagePoolConfig{Tag: configured.Tag, Strategy: configured.Strategy}
	for _, candidate := range configured.Templates {
		pool.Templates = append(pool.Templates, applicationoperator.MessageTemplateConfig{
			Tag: candidate.Tag, Template: candidate.Template,
		})
	}
	return pool
}

func applicationTailoringProcessor(profile appconfig.Profile, models map[string]applicationoperator.ApplicationMessageModel) (applicationoperator.ResumeTailoringProcessor, error) {
	skills, enabled := profile.Applications.TailoringSkills()
	if !enabled {
		return nil, nil
	}
	deterministic, err := applicationoperator.NewAddVacancySkillsProcessor("skills-from-vacancy", "v1", skills.Maximum)
	if err != nil {
		return nil, err
	}
	if skills.Model == nil {
		return deterministic, nil
	}
	provider := strings.TrimSpace(skills.Model.Provider)
	candidate := models[provider]
	if candidate == nil {
		return nil, fmt.Errorf("application tailoring model references unavailable provider %q", provider)
	}
	tailoringModel, ok := candidate.(applicationoperator.ResumeTailoringModel)
	if !ok {
		return nil, fmt.Errorf("application tailoring model provider %q does not support skill selection", provider)
	}
	timeout, err := time.ParseDuration(skills.Model.Timeout)
	if err != nil || timeout <= 0 {
		return nil, fmt.Errorf("application tailoring model provider %q has invalid timeout %q", provider, skills.Model.Timeout)
	}
	return applicationoperator.NewModelResumeTailoringProcessor(applicationoperator.ModelResumeTailoringConfig{
		Tag: provider, PromptVersion: skills.Model.PromptVersion, Instruction: skills.Model.Instruction,
		MaximumSkills: skills.Maximum, Timeout: timeout, Model: tailoringModel, Fallback: deterministic,
	})
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
		if _, err := handler.EnsureRun(ctx, run, search.Priority); err != nil {
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
				Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
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
				Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
			})
		}
	}
	return definitions, nil
}

func resumePublishDefinitions(cfg appconfig.Config, instances map[string]adapter.Adapter, publishers *taskworker.ResumePublisherRegistry) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionResumePublish {
			continue
		}
		profile := profiles[job.Action.Profile]
		profileID := core.ProfileID(profile.Tag)
		if !profile.Enabled || !publishers.Has(profileID) {
			continue
		}
		resumeID := job.Action.Resume
		if resumeID == "" {
			resumeID = profile.Resume
		}
		payload, err := json.Marshal(core.ResumePublishPayload{ProfileID: profileID, ResumeID: resumeID})
		if err != nil {
			return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
		}
		instance := instances[profile.Adapter]
		for index, trigger := range job.Triggers {
			minimum, maximum := trigger.Jitter.Durations()
			definitions = append(definitions, jobscheduler.Definition{
				JobTag: job.Tag, TriggerIndex: index, Expression: trigger.Expression, Timezone: trigger.Timezone,
				ActionType: core.TaskResumePublish, Platform: core.Platform(instance.Name()), ProfileID: profileID,
				Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
			})
		}
	}
	return definitions, nil
}

func profileActivityDefinitions(cfg appconfig.Config, instances map[string]adapter.Adapter, observers *taskworker.ProfileActivityObserverRegistry) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionProfileActivityObserve {
			continue
		}
		profile := profiles[job.Action.Profile]
		profileID := core.ProfileID(profile.Tag)
		if !profile.Enabled || !observers.Has(profileID) {
			continue
		}
		resumeID := job.Action.Resume
		if resumeID == "" {
			resumeID = profile.Resume
		}
		payload, err := json.Marshal(core.ProfileActivityObservePayload{ProfileID: profileID, ResumeID: resumeID})
		if err != nil {
			return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
		}
		instance := instances[profile.Adapter]
		for index, trigger := range job.Triggers {
			minimum, maximum := trigger.Jitter.Durations()
			definitions = append(definitions, jobscheduler.Definition{
				JobTag: job.Tag, TriggerIndex: index, Expression: trigger.Expression, Timezone: trigger.Timezone,
				ActionType: core.TaskProfileActivityObserve, Platform: core.Platform(instance.Name()), ProfileID: profileID,
				Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
			})
		}
	}
	return definitions, nil
}

func conversationDiscoveryDefinitions(cfg appconfig.Config, instances map[string]adapter.Adapter, transports *taskworker.ConversationTransportRegistry) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionConversationSync {
			continue
		}
		profile := profiles[job.Action.Profile]
		profileID := core.ProfileID(profile.Tag)
		if !profile.Enabled || !transports.CanDiscover(profileID) {
			continue
		}
		payload, err := json.Marshal(core.ConversationDiscoverPayload{ProfileID: profileID})
		if err != nil {
			return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
		}
		instance := instances[profile.Adapter]
		for index, trigger := range job.Triggers {
			minimum, maximum := trigger.Jitter.Durations()
			definitions = append(definitions, jobscheduler.Definition{
				JobTag: job.Tag, TriggerIndex: index, Expression: trigger.Expression, Timezone: trigger.Timezone,
				ActionType: core.TaskConversationDiscover, Platform: core.Platform(instance.Name()), ProfileID: profileID,
				Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
			})
		}
	}
	return definitions, nil
}

func conversationFollowUpSelectionDefinitions(cfg appconfig.Config, instances map[string]adapter.Adapter, transports *taskworker.ConversationTransportRegistry) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionConversationFollowUpSelect {
			continue
		}
		profile := profiles[job.Action.Profile]
		if !profile.Enabled || job.Action.FollowUp == nil || !profile.Conversations.AllowSend {
			continue
		}
		profileID := core.ProfileID(profile.Tag)
		if !transports.Has(profileID) {
			continue
		}
		instance := instances[profile.Adapter]
		capabilities, err := core.NewCapabilitySet(instance.Capabilities()...)
		if err != nil {
			return nil, fmt.Errorf("adapter %q capabilities: %w", profile.Adapter, err)
		}
		if !capabilities.Supports(core.CapabilityConversationWrite) {
			continue
		}
		payload, err := json.Marshal(job.Action.FollowUp.Payload(profileID))
		if err != nil {
			return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
		}
		for index, trigger := range job.Triggers {
			minimum, maximum := trigger.Jitter.Durations()
			definitions = append(definitions, jobscheduler.Definition{
				JobTag: job.Tag, TriggerIndex: index, Expression: trigger.Expression, Timezone: trigger.Timezone,
				ActionType: core.TaskConversationFollowUpSelect, Platform: core.Platform(instance.Name()), ProfileID: profileID,
				Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
			})
		}
	}
	return definitions, nil
}

func applicationRetentionDefinitions(cfg appconfig.Config, instances map[string]adapter.Adapter, observers *taskworker.ApplicationStateObserverRegistry) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionApplicationRetention || job.Action.Retention == nil {
			continue
		}
		profile := profiles[job.Action.Profile]
		profileID := core.ProfileID(profile.Tag)
		if !profile.Enabled {
			continue
		}
		if !observers.Has(profileID) {
			return nil, fmt.Errorf("job %q: application retention requires an authorized application state observer; HH currently needs OAuth/API credentials", job.Tag)
		}
		payload, err := json.Marshal(job.Action.Retention.Payload(profileID))
		if err != nil {
			return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
		}
		instance := instances[profile.Adapter]
		for index, trigger := range job.Triggers {
			minimum, maximum := trigger.Jitter.Durations()
			definitions = append(definitions, jobscheduler.Definition{
				JobTag: job.Tag, TriggerIndex: index, Expression: trigger.Expression, Timezone: trigger.Timezone,
				ActionType: core.TaskApplicationRetention, Platform: core.Platform(instance.Name()), ProfileID: profileID,
				Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
			})
		}
	}
	return definitions, nil
}

func profileStateReconcileDefinitions(
	cfg appconfig.Config,
	resources []core.ProfileStateResource,
	readers map[core.ProfileID]adapter.ProfileStateReader,
	platforms map[core.ProfileID]core.Platform,
) ([]jobscheduler.Definition, error) {
	resourcesByTag := make(map[string]core.ProfileStateResource, len(resources))
	for _, resource := range resources {
		resourcesByTag[resource.Tag] = resource
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionProfileStateReconcile {
			continue
		}
		resource, exists := resourcesByTag[job.Action.Resource]
		if !exists {
			return nil, fmt.Errorf("job %q references unknown profile state resource %q", job.Tag, job.Action.Resource)
		}
		platform, writable := platforms[resource.ProfileID]
		if readers[resource.ProfileID] == nil || !writable {
			continue
		}
		payload, err := json.Marshal(core.ProfileStateReconcilePayload{ResourceTag: resource.Tag})
		if err != nil {
			return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
		}
		for index, trigger := range job.Triggers {
			minimum, maximum := trigger.Jitter.Durations()
			definitions = append(definitions, jobscheduler.Definition{
				JobTag: job.Tag, TriggerIndex: index, Expression: trigger.Expression, Timezone: trigger.Timezone,
				ActionType: core.TaskProfileStateReconcile, Platform: platform, ProfileID: resource.ProfileID,
				Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
			})
		}
	}
	return definitions, nil
}

func conversationWorkers(consumer broker.TaskConsumer, handlers *taskworker.ConversationHandlers, lane *taskworker.ProfileMutationLane) ([]*taskworker.Worker, error) {
	if lane == nil {
		return nil, errors.New("conversation workers require profile mutation lane")
	}
	definitions := []struct {
		taskType core.TaskType
		handler  taskworker.HandlerFunc
	}{
		{core.TaskConversationSend, lane.Wrap(handlers.Send)},
		{core.TaskConversationFollowUp, lane.Wrap(handlers.FollowUp)},
		{core.TaskConversationMarkRead, lane.Wrap(handlers.MarkRead)},
		{core.TaskConversationDiscover, handlers.Discover},
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
	return newTaskWorkerBlockedBy(consumer, taskType, "", handler)
}

func newTaskWorkerBlockedBy(consumer broker.TaskConsumer, taskType, blockerType core.TaskType, handler taskworker.HandlerFunc) (*taskworker.Worker, error) {
	return taskworker.New(consumer, handler, taskworker.SystemClock{}, taskworker.Config{
		ID: "worker-" + string(taskType), TaskType: taskType, BlockedByTaskType: blockerType,
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
