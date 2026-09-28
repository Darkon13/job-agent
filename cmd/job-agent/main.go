package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/adapters/hh"
	"github.com/Darkon13/job-agent/api/httpapi"
	"github.com/Darkon13/job-agent/auth"
	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/browser"
	"github.com/Darkon13/job-agent/browsercheck"
	"github.com/Darkon13/job-agent/buildinfo"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	toolbrowserstate "github.com/Darkon13/job-agent/internal/tools/browserstate"
	toolcheck "github.com/Darkon13/job-agent/internal/tools/check"
	applicationoperator "github.com/Darkon13/job-agent/operator"
	"github.com/Darkon13/job-agent/operator/openaichat"
	"github.com/Darkon13/job-agent/operator/openairesponses"
	jobscheduler "github.com/Darkon13/job-agent/scheduler"
	"github.com/Darkon13/job-agent/storage"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
	taskworker "github.com/Darkon13/job-agent/worker"
	"github.com/Darkon13/job-agent/workflow"
)

type mainOptions struct {
	configPath string
	migrateUp  bool
}

// profileStateFiles maps enabled profiles to their configured browser state
// file so a dashboard login can store the session without knowing paths.
func profileStateFiles(cfg appconfig.Config) map[core.ProfileID]string {
	files := make(map[core.ProfileID]string, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		if !profile.Enabled || strings.TrimSpace(profile.StateFile) == "" {
			continue
		}
		files[core.ProfileID(profile.Tag)] = profile.StateFile
	}
	return files
}

// dashboardProfiles lists enabled profiles for the dashboard account switcher
// with the resolved sender name. It exposes configuration identities and the
// display name only, never credentials.
func dashboardProfiles(cfg appconfig.Config, contacts map[core.ProfileID]applicationoperator.ApplicationProfileContext) []httpapi.ProfileSummary {
	profiles := make([]httpapi.ProfileSummary, 0, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		if !profile.Enabled {
			continue
		}
		profileID := core.ProfileID(profile.Tag)
		resolved := contacts[profileID]
		displayName := strings.TrimSpace(strings.TrimSpace(resolved.FirstName) + " " + strings.TrimSpace(resolved.LastName))
		if displayName == "" && profile.Identity != nil {
			// The cached identity keeps the account recognizable before the
			// first live contact read after a restart.
			displayName = strings.TrimSpace(profile.Identity.DisplayName)
		}
		profiles = append(profiles, httpapi.ProfileSummary{ID: profileID, DisplayName: displayName, Resumes: len(profile.Resumes)})
	}
	return profiles
}

// profileCatalog builds the operator-facing profile list. It is derived from
// the config (including dashboard-managed fragments) and never from a live
// platform call, so the list is available without a login.
func profileCatalog(cfg appconfig.Config, instances map[string]adapter.Adapter) []httpapi.ProfileCatalogEntry {
	entries := make([]httpapi.ProfileCatalogEntry, 0, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		platform := ""
		if instance := instances[profile.Adapter]; instance != nil {
			platform = instance.Name()
		}
		source := profile.Source
		if source == "" {
			source = appconfig.ProfileSourceConfig
		}
		entry := httpapi.ProfileCatalogEntry{
			Tag: profile.Tag, Adapter: profile.Adapter, Platform: platform,
			Enabled: profile.Enabled, Source: source,
			Session: httpapi.ProfileCatalogSession{StateFile: profile.StateFile},
		}
		for _, resume := range profile.Resumes {
			entry.Resumes = append(entry.Resumes, httpapi.ProfileCatalogResume{
				ID: resume.ID, Title: resume.Title, Primary: resume.Primary,
			})
		}
		if profile.Identity != nil {
			entry.Identity = &httpapi.ProfileCatalogIdentity{
				DisplayName: profile.Identity.DisplayName, Email: profile.Identity.Email,
				Phone: profile.Identity.Phone, AccountHash: profile.Identity.AccountHash,
				CapturedAt: profile.Identity.CapturedAt,
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// liveConversationSync mirrors the durable conversation.sync worker for the
// interactive dashboard path: read through the profile transport, store the
// presentation and messages, and let the background task chain handle
// auto-answers.
type liveConversationSync struct {
	repository *storesqlite.Store
	transports *taskworker.ConversationTransportRegistry
	workflow   *workflow.ConversationWorkflow
}

func (sync liveConversationSync) SyncConversationNow(ctx context.Context, conversationID core.ConversationID) error {
	conversation, err := sync.repository.Conversation(ctx, conversationID)
	if err != nil {
		return err
	}
	transport, err := sync.transports.Resolve(conversation.ProfileID)
	if err != nil {
		return err
	}
	result, err := transport.SyncConversation(ctx, conversation.ProfileID, conversation.ID, conversation.ExternalID)
	if err != nil {
		return err
	}
	if result.ObservedAt.IsZero() {
		return errors.New("conversation sync returned zero observation time")
	}
	if err := sync.workflow.ObserveConversationPresentation(ctx, conversation.ID, result.Presentation, result.ObservedAt); err != nil {
		return err
	}
	for _, message := range result.Messages {
		if _, _, err := sync.repository.AppendConversationMessage(ctx, message, result.ObservedAt); err != nil {
			if !errors.Is(err, storage.ErrConversationMessageConflict) {
				return err
			}
		}
	}
	return nil
}

// liveConversationSend runs the durable conversation.send handler inline for
// the dashboard. The synthetic task reuses the same idempotency key as the
// queue would, so a fallback enqueue cannot duplicate the platform message.
type liveConversationSend struct {
	repository *storesqlite.Store
	handlers   *taskworker.ConversationHandlers
}

func (sender liveConversationSend) SendConversationNow(ctx context.Context, conversationID core.ConversationID, content core.MessageContent, replyToID core.MessageID, requestKey string) error {
	conversation, err := sender.repository.Conversation(ctx, conversationID)
	if err != nil {
		return err
	}
	key, err := core.ConversationSendIdempotencyKey(conversationID, string(core.TaskConversationSend)+"\x00"+requestKey)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(core.ConversationSendPayload{ConversationID: conversationID, ReplyToID: replyToID, Content: content})
	if err != nil {
		return err
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID("task-live-" + requestKey), Type: core.TaskConversationSend, IdempotencyKey: key,
		Source: "dashboard-live", Platform: conversation.Platform, ProfileID: conversation.ProfileID,
		CorrelationID: core.CorrelationID("dashboard-live-" + requestKey), Payload: payload,
		AvailableAt: time.Now().UTC(),
	}, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := sender.handlers.Send(ctx, task); err != nil {
		return err
	}
	current, err := sender.repository.Conversation(ctx, conversationID)
	if err != nil {
		return err
	}
	if current.Status != core.ConversationActive {
		return httpapi.ErrConversationClosed
	}
	return nil
}

// mainUsage lists the service invocation and every facade subcommand. The
// historical utilities are dispatched through runUtility; each one keeps its
// own detailed usage message.
const mainUsage = `job-agent — сервис автоматизации поиска работы.

Запуск сервиса:
  job-agent [-migrate-up] <config.json>       backend, scheduler и HTTP API
  job-agent-dashboard                         dashboard на localhost
  job-agent-migrate [-config FILE] up|down    миграции базы

Подкоманды фасада:
  check <config.json>                         проверить конфиг, ключи моделей и базу
  browser-state sanitize <state.json>         сжать browser storage state
  auth login|import|status|logout ...         вход в HeadHunter
  jobs list|pause|resume ...                  список джоб и пауза
  profile list|show ...                       профили, резюме и состояние сессии
  captcha solve <application_id>              пройти проверку HH в браузере вручную
  captcha list                                отклики, ожидающие капчу
  db backup|restore ...                       обслуживание базы
  startup ...                                 однократная сверка при старте

Запуск job вручную, проверки и опросники, тесты и bootstrap профиля живут в
dashboard; CLI-формы для них временно убраны.

  help | --help                               эта справка
  --version                                   версия, commit и время сборки

Подробности: https://darkon13.github.io/job-agent/
`

// runUtility dispatches the historical utility binaries that now live behind
// the job-agent facade. Unknown names return handled=false so the server flags
// keep working.
func runUtility(name string, args []string) (int, bool) {
	switch name {
	case "check":
		return toolcheck.Run(args), true
	case "browser-state":
		return toolbrowserstate.Run(args), true
	default:
		return 0, false
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if buildinfo.Requested(os.Args[1:]) {
		if err := buildinfo.Write("job-agent", os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Fprint(os.Stdout, mainUsage)
		return
	}
	if len(os.Args) > 1 {
		if code, handled := runUtility(os.Args[1], os.Args[2:]); handled {
			os.Exit(code)
		}
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
	if len(os.Args) >= 2 && os.Args[1] == "jobs" {
		if err := runJobs(context.Background(), os.Args[2:], os.Stdout, nil); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "profile" {
		if err := runProfile(context.Background(), os.Args[2:], os.Stdout, nil); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "captcha" {
		if err := runCaptcha(context.Background(), os.Args[2:], os.Stdout, nil); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "db" {
		if err := runDB(context.Background(), os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	options, err := parseMainOptions(os.Args[1:])
	if err != nil {
		fmt.Fprint(os.Stderr, mainUsage)
		log.Fatalf("error: %v", err)
	}
	if _, err := os.Stat(options.configPath); err != nil {
		fmt.Fprint(os.Stderr, mainUsage)
		log.Fatalf("error: config %s: %v", options.configPath, err)
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
	apiToken, err := resolveAPIToken(cfg.Server.APITokenEnv, os.LookupEnv)
	if err != nil {
		log.Fatalf("resolve API token: %v", err)
	}
	employerMatcher, err := cfg.BuildEmployerGroupMatcher()
	if err != nil {
		log.Fatalf("build employer groups: %v", err)
	}
	applicationModels, err := buildApplicationModels(cfg.Models, os.LookupEnv)
	if err != nil {
		log.Fatalf("build application models: %v", err)
	}
	var answerRegistry *core.AnswerBlockRegistry
	if blocks := cfg.ResolvedAnswerBlocks(); len(blocks) > 0 {
		answerRegistry, err = core.NewAnswerBlockRegistry(blocks...)
		if err != nil {
			log.Fatalf("build answer block registry: %v", err)
		}
	} else {
		// The vacancy review pipeline works from an empty registry: the first
		// human answer creates the conventional reviewed block.
		answerRegistry, err = core.NewAnswerBlockRegistry()
		if err != nil {
			log.Fatalf("build empty answer block registry: %v", err)
		}
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
			logf("close database: %v", err)
		}
	}()
	if recovered, err := store.FailOpenAuthSessions(context.Background(), time.Now().UTC(), "backend restarted; start a new login"); err != nil {
		log.Fatalf("recover auth sessions: %v", err)
	} else if recovered > 0 {
		logf("marked %d interrupted auth sessions as failed after restart", recovered)
	}
	var answerResolver taskworker.AnswerBlockResolver
	if len(cfg.ResolvedAnswerBlocks()) > 0 {
		answerResolver, err = taskworker.NewReviewedVacancyAnswers(answerRegistry, store)
		if err != nil {
			log.Fatalf("build reviewed vacancy answers: %v", err)
		}
	}
	vacancyAnswerResolver, err := taskworker.NewReviewedVacancyAnswers(answerRegistry, store)
	if err != nil {
		log.Fatalf("build reviewed vacancy answers: %v", err)
	}

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
			logf("profile %q requires authentication; profile workers are disabled", profileID)
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
	reviewAPI, err := httpapi.NewReviewAPI(store, vacancyTestWorkflow, store)
	if err != nil {
		log.Fatalf("create review API: %v", err)
	}
	reviewAPI.ConfigureAnswerBank(answerResolver)
	resumeAPI, err := httpapi.NewResumeAPI(cfg.ResumeTargets())
	if err != nil {
		log.Fatalf("create resume API: %v", err)
	}
	qualificationWorkflow, err := workflow.NewQualificationWorkflow(store, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	if err != nil {
		log.Fatalf("create qualification workflow: %v", err)
	}
	resumeUpdateWorkflow, err := workflow.NewResumeUpdateWorkflow(store, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	if err != nil {
		log.Fatalf("create resume update workflow: %v", err)
	}
	resumeAPI.ConfigureUpdate(resumeUpdateWorkflow)
	profileCatalogAPI, err := httpapi.NewProfileCatalogAPI(profileCatalog(cfg, instances))
	if err == nil {
		profileCatalogAPI.SetIdentitySource(store)
	}
	if err != nil {
		log.Fatalf("create profile catalog API: %v", err)
	}
	conversationAPI, err := httpapi.NewConversationAPI(store, conversationWorkflow)
	if err != nil {
		log.Fatalf("create conversation API: %v", err)
	}
	profileContacts := resolveProfileContacts(cfg, instances)
	runtimeAPI, err := httpapi.NewRuntimeAPI(store, dashboardProfiles(cfg, profileContacts))
	if err == nil {
		runtimeAPI.ConfigureSummaryCache(3 * time.Second)
		runtimeAPI.SetStatePaths(profileStateFiles(cfg))
	}
	if err != nil {
		log.Fatalf("create runtime API: %v", err)
	}
	taskControlWorkflow, err := workflow.NewTaskControlWorkflow(store, workflow.SystemClock{})
	if err != nil {
		log.Fatalf("create task control workflow: %v", err)
	}
	// A missing platform session cannot heal by itself: the guard stops the job
	// that failed with unauthorized, and the next successful sign-in resumes
	// those jobs and retries the failures.
	authGuard, err := workflow.NewAuthGuard(store, store, taskControlWorkflow, workflow.SystemClock{})
	if err != nil {
		log.Fatalf("create auth guard: %v", err)
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
	applicationRetryWorkflow, err := workflow.NewApplicationRetryWorkflow(
		store, store, workflow.SystemClock{}, workflow.RandomIDGenerator{},
	)
	if err != nil {
		log.Fatalf("create application retry workflow: %v", err)
	}
	applicationAPI, err := httpapi.NewApplicationAPI(applicationRemovalWorkflow, applicationRetryWorkflow)
	if err != nil {
		log.Fatalf("create application API: %v", err)
	}
	browserClient, err := browserWorkerHTTPClient()
	if err != nil {
		log.Fatalf("create browser worker client: %v", err)
	}
	var browserSubmissionDriver *hh.BrowserSubmissionDriver
	if browserClient != nil {
		submissionDriver, err := hh.NewBrowserSubmissionDriver(browserClient)
		if err != nil {
			log.Fatalf("create browser submission driver: %v", err)
		}
		browserSubmissionDriver = submissionDriver
		browserCheckService, err := browsercheck.NewService(
			submissionDriver, store, applicationRetryWorkflow, workflow.SystemClock{}, workflow.RandomIDGenerator{},
		)
		if err != nil {
			log.Fatalf("create browser check service: %v", err)
		}
		applicationAPI.ConfigureBrowserCheck(browserCheckService)
	} else {
		logf("browser worker is not configured; interactive browser checks are disabled")
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
	// Only a profile whose account state can be read may import it.
	profileImportPlatforms := make(map[core.ProfileID]core.Platform)
	resumeTouchers := taskworker.NewResumeToucherRegistry()
	sessionRefreshers := taskworker.NewSessionRefresherRegistry()
	resumePublishers := taskworker.NewResumePublisherRegistry()
	testCapturers := taskworker.NewVacancyTestCapturerRegistry()
	testSubmitters := taskworker.NewVacancyTestSubmitterRegistry()
	qualificationReaders := taskworker.NewQualificationCatalogRegistry()
	qualificationAttempts := taskworker.NewQualificationAttemptRegistry()
	qualificationPlatforms := make(map[core.ProfileID]core.Platform)
	qualificationAnswerModels := make(map[core.ProfileID]taskworker.QualificationAnswerModel)
	activityObservers := taskworker.NewProfileActivityObserverRegistry()
	applicationPlans := taskworker.NewLiveApplicationPlans()
	applicationTailoringPlans := make(map[core.ProfileID]taskworker.ApplicationTailoringPlan)
	knownConversationAnswers := newKnownAnswerProfiles()
	binder := &profileRuntimeBinder{
		instances: instances, profiles: profiles, contacts: profileContacts,
		applicationModels: applicationModels, employerMatcher: employerMatcher,
		answerResolver: answerResolver, browserSubmissionDriver: browserSubmissionDriver,
		knownAnswers: knownConversationAnswers, applicationPlans: applicationPlans,
		profileStateReaders: profileStateReaders, profileStateWriters: profileStateWriters,
		profileStatePlatforms: profileStatePlatforms, conversationTransports: conversationTransports,
		applicationTransports: applicationTransports, applicationStateObservers: applicationStateObservers,
		profileImportPlatforms: profileImportPlatforms,
		resumeTouchers:         resumeTouchers, resumePublishers: resumePublishers,
		testCapturers: testCapturers, testSubmitters: testSubmitters,
		qualificationReaders: qualificationReaders, qualificationAttempts: qualificationAttempts,
		qualificationPlatforms: qualificationPlatforms, qualificationAnswerModels: qualificationAnswerModels,
		activityObservers: activityObservers, applicationTailoringPlans: applicationTailoringPlans,
	}
	for _, profile := range cfg.Profiles {
		preparer, err := applicationPreparer(profile, employerMatcher, applicationModels, profileContacts[core.ProfileID(profile.Tag)])
		if err != nil {
			log.Fatalf("build application operator for profile %q: %v", profile.Tag, err)
		}
		if !profile.Enabled {
			continue
		}
		if err := binder.bind(profile, preparer, false); err != nil {
			log.Fatalf("bind profile %q: %v", profile.Tag, err)
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
	profileImportWorkflow, err := workflow.NewProfileImportWorkflow(
		store, workflow.SystemClock{}, workflow.RandomIDGenerator{}, profileImportPlatforms,
	)
	if err != nil {
		log.Fatalf("create profile import workflow: %v", err)
	}
	profileCatalogAPI.SetImporter(profileImportWorkflow)
	profileStateAPI, err := httpapi.NewProfileStateAPI(
		profileStatePlanner, profileStateApplyWorkflow, profileStateReconcileWorkflow, store, store, profileStateReaders,
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
	conversationAPI.ConfigureLiveReader(liveConversationSync{
		repository: store, transports: conversationTransports, workflow: conversationWorkflow,
	})
	conversationAPI.ConfigureLiveSender(liveConversationSend{repository: store, handlers: conversationHandlers})
	if answerRegistry != nil && knownConversationAnswers.Len() > 0 {
		conversationHandlers.ConfigureKnownAnswers(answerRegistry, func(profileID core.ProfileID) bool {
			return knownConversationAnswers.Has(profileID)
		})
		logf("known conversation answers are enabled for %d profile(s)", knownConversationAnswers.Len())
	} else if knownConversationAnswers.Len() > 0 {
		logf("WARNING: conversations.answer_known is set but no answer_sets are resolved; no automatic conversation answers will be sent")
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
		profileStateHandler, err := taskworker.NewProfileStateApplyHandler(store, store, profileStateWriters, taskworker.SystemClock{})
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
	if applicationPlans.Len() > 0 && applicationTransports.VacancyReaderCount() > 0 {
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
		store, store, store, store, conversationTransports, applicationStateObservers, taskworker.SystemClock{},
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
		store, store, applicationStateObservers, applicationRemovalWorkflow, taskworker.SystemClock{},
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
	applicationStateSyncHandler, err := taskworker.NewApplicationStateSyncHandler(
		store, applicationStateObservers, taskworker.SystemClock{},
	)
	if err != nil {
		log.Fatalf("create application state sync handler: %v", err)
	}
	applicationStateSyncWorker, err := newTaskWorker(
		store, core.TaskApplicationStateSync, applicationStateSyncHandler.Handle,
	)
	if err != nil {
		log.Fatalf("create application state sync worker: %v", err)
	}
	workers = append(workers, applicationStateSyncWorker)
	profileImportHandler, err := taskworker.NewProfileImportHandler(
		store, store, store, applicationStateObservers, store, workflow.RandomIDGenerator{}, taskworker.SystemClock{},
	)
	if err != nil {
		log.Fatalf("create profile import handler: %v", err)
	}
	profileImportWorker, err := newTaskWorker(store, core.TaskProfileStateImport, profileImportHandler.Handle)
	if err != nil {
		log.Fatalf("create profile import worker: %v", err)
	}
	workers = append(workers, profileImportWorker)
	activityMaintainHandler, err := taskworker.NewActivityMaintainHandler(
		store, applicationTransports, store, store, taskworker.SystemClock{},
	)
	if err != nil {
		log.Fatalf("create activity maintain handler: %v", err)
	}
	activityMaintainWorker, err := newTaskWorker(store, core.TaskProfileActivityMaintain, activityMaintainHandler.Handle)
	if err != nil {
		log.Fatalf("create activity maintain worker: %v", err)
	}
	workers = append(workers, activityMaintainWorker)
	validationRefreshHandler, err := taskworker.NewValidationRefreshHandler(
		store, store, applicationTransports, taskworker.SystemClock{},
	)
	if err != nil {
		log.Fatalf("create validation refresh handler: %v", err)
	}
	validationRefreshWorker, err := newTaskWorker(store, core.TaskApplicationValidationCheck, validationRefreshHandler.Handle)
	if err != nil {
		log.Fatalf("create validation refresh worker: %v", err)
	}
	workers = append(workers, validationRefreshWorker)
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
	if refreshClient, err := browserWorkerHTTPClient(); err != nil {
		logf("profile session refresh is disabled: %v", err)
	} else if refreshClient != nil {
		for _, profile := range cfg.Profiles {
			if !profile.Enabled || strings.TrimSpace(profile.StateFile) == "" {
				continue
			}
			if instances[profile.Adapter].Name() != hh.Name {
				continue
			}
			profileID := core.ProfileID(profile.Tag)
			if err := sessionRefreshers.Register(profileID, refreshClient, profile.StateFile); err != nil {
				log.Fatalf("register session refresher for profile %q: %v", profile.Tag, err)
			}
		}
		if sessionRefreshers.Count() > 0 {
			sessionHandler, err := taskworker.NewSessionRefreshHandler(sessionRefreshers, func(data []byte) ([]byte, error) {
				sanitized, _, err := hh.SanitizeBrowserStorageStateData(data)
				return sanitized, err
			})
			if err != nil {
				log.Fatalf("create session refresh handler: %v", err)
			}
			sessionWorker, err := newTaskWorker(store, core.TaskProfileSessionRefresh, sessionHandler.Handle)
			if err != nil {
				log.Fatalf("create session refresh worker: %v", err)
			}
			workers = append(workers, sessionWorker)
			logf("profile session refresh is enabled for %d profile(s)", sessionRefreshers.Count())
		}
	}
	if testCapturers.Count() > 0 {
		testCaptureHandler, err := taskworker.NewVacancyTestCaptureHandler(testCapturers, store, taskworker.SystemClock{})
		if err != nil {
			log.Fatalf("create vacancy test capture handler: %v", err)
		}
		testCaptureHandler.ConfigureAnswerRouting(vacancyAnswerResolver, store, vacancyTestWorkflow)
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
		questionnaireAnswerHandler.ConfigureChain(vacancyTestWorkflow)
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
	if qualificationReaders.Count() > 0 {
		qualificationSyncHandler, err := taskworker.NewQualificationSyncHandler(qualificationReaders, store)
		if err != nil {
			log.Fatalf("create qualification sync handler: %v", err)
		}
		qualificationSyncWorker, err := newTaskWorker(store, core.TaskSkillVerificationSync, qualificationSyncHandler.Handle)
		if err != nil {
			log.Fatalf("create qualification sync worker: %v", err)
		}
		workers = append(workers, qualificationSyncWorker)
	}
	if qualificationAttempts.Count() > 0 && answerResolver != nil {
		qualificationStartHandler, err := taskworker.NewQualificationStartHandler(
			qualificationAttempts, store, store, store, answerResolver, store, store, taskworker.SystemClock{},
		)
		if err != nil {
			log.Fatalf("create qualification start handler: %v", err)
		}
		qualificationStartHandler.ConfigureAnswerModels(qualificationAnswerModels)
		qualificationStartWorker, err := newTaskWorker(store, core.TaskSkillVerificationStart, qualificationStartHandler.Handle)
		if err != nil {
			log.Fatalf("create qualification start worker: %v", err)
		}
		workers = append(workers, qualificationStartWorker)
	}
	reviewAnswerHandler, err := taskworker.NewReviewAnswerHandler(store, taskworker.SystemClock{})
	if err != nil {
		log.Fatalf("create review answer handler: %v", err)
	}
	reviewAnswerHandler.ConfigureContinuation(vacancyAnswerResolver, store, vacancyTestWorkflow)
	reviewAnswerWorker, err := newTaskWorker(store, core.TaskReviewAnswer, reviewAnswerHandler.Handle)
	if err != nil {
		log.Fatalf("create review answer worker: %v", err)
	}
	workers = append(workers, reviewAnswerWorker)
	answerCoveredHandler, err := taskworker.NewApplicationAnswerCoveredHandler(
		store, vacancyAnswerResolver, vacancyTestWorkflow, taskworker.SystemClock{},
	)
	if err != nil {
		log.Fatalf("create application answer covered handler: %v", err)
	}
	answerCoveredWorker, err := newTaskWorker(store, core.TaskApplicationAnswerCovered, answerCoveredHandler.Handle)
	if err != nil {
		log.Fatalf("create application answer covered worker: %v", err)
	}
	workers = append(workers, answerCoveredWorker)
	if profileStateWriters.Count() > 0 {
		resumeUpdateHandler, err := taskworker.NewResumeUpdateHandler(profileStatePlanner, profileStateReaders, profileStateWriters, resumePublishers, store, taskworker.SystemClock{})
		if err != nil {
			log.Fatalf("create resume update handler: %v", err)
		}
		resumeUpdateWorker, err := newTaskWorker(store, core.TaskResumeUpdate, profileMutationLane.Wrap(resumeUpdateHandler.Handle))
		if err != nil {
			log.Fatalf("create resume update worker: %v", err)
		}
		workers = append(workers, resumeUpdateWorker)
	}
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
	buildReloadableDefinitions := func(cfg appconfig.Config) ([]jobscheduler.Definition, error) {
		definitions, err := resumeTouchDefinitions(cfg, instances, resumeTouchers)
		if err != nil {
			return nil, fmt.Errorf("build scheduled jobs: %w", err)
		}
		resumePublishDefinitions, err := resumePublishDefinitions(cfg, instances, resumePublishers)
		if err != nil {
			return nil, fmt.Errorf("build resume publish scheduled jobs: %w", err)
		}
		definitions = append(definitions, resumePublishDefinitions...)
		sessionRefreshScheduled, err := sessionRefreshDefinitions(cfg, sessionRefreshers)
		if err != nil {
			return nil, fmt.Errorf("build profile session refresh jobs: %w", err)
		}
		definitions = append(definitions, sessionRefreshScheduled...)
		activityDefinitions, err := profileActivityDefinitions(cfg, instances, activityObservers)
		if err != nil {
			return nil, fmt.Errorf("build profile activity scheduled jobs: %w", err)
		}
		definitions = append(definitions, activityDefinitions...)
		conversationDefinitions, err := conversationDiscoveryDefinitions(cfg, instances, conversationTransports)
		if err != nil {
			return nil, fmt.Errorf("build conversation sync jobs: %w", err)
		}
		definitions = append(definitions, conversationDefinitions...)
		systemDefinitions, err := profileSystemDefinitions(cfg, profileSystemCapabilities{
			sessionRefresh:     sessionRefreshers.Has,
			resumeTouch:        resumeTouchers.Has,
			applicationCleanup: applicationStateObservers.Has,
			validationCheck: func(profileID core.ProfileID) bool {
				_, resolveErr := applicationTransports.ResolveVacancyReader(profileID)
				return resolveErr == nil
			},
			activityMaintain: func(profileID core.ProfileID) bool {
				_, resolveErr := applicationTransports.ResolveVacancyReader(profileID)
				return resolveErr == nil
			},
		})
		if err != nil {
			return nil, fmt.Errorf("build system profile jobs: %w", err)
		}
		definitions = append(definitions, systemDefinitions...)
		followUpSelectionDefinitions, err := conversationFollowUpSelectionDefinitions(cfg, instances, conversationTransports)
		if err != nil {
			return nil, fmt.Errorf("build conversation follow-up selection jobs: %w", err)
		}
		definitions = append(definitions, followUpSelectionDefinitions...)
		retentionDefinitions, err := applicationRetentionDefinitions(cfg, instances, applicationStateObservers)
		if err != nil {
			return nil, fmt.Errorf("build application retention scheduled jobs: %w", err)
		}
		definitions = append(definitions, retentionDefinitions...)
		stateSyncDefinitions, err := applicationStateSyncDefinitions(cfg, instances, applicationStateObservers)
		if err != nil {
			return nil, fmt.Errorf("build application state sync scheduled jobs: %w", err)
		}
		definitions = append(definitions, stateSyncDefinitions...)
		activityMaintainDefinitions, err := profileActivityMaintainDefinitions(cfg, instances, applicationTransports)
		if err != nil {
			return nil, fmt.Errorf("build profile activity maintain scheduled jobs: %w", err)
		}
		definitions = append(definitions, activityMaintainDefinitions...)
		answerCoveredDefinitions, err := applicationAnswerCoveredDefinitions(cfg, instances)
		if err != nil {
			return nil, fmt.Errorf("build application answer covered scheduled jobs: %w", err)
		}
		definitions = append(definitions, answerCoveredDefinitions...)
		validationRefreshDefinitions, err := applicationValidationRefreshDefinitions(cfg, instances, applicationTransports)
		if err != nil {
			return nil, fmt.Errorf("build application validation refresh scheduled jobs: %w", err)
		}
		definitions = append(definitions, validationRefreshDefinitions...)
		profileStateDefinitions, err := profileStateReconcileDefinitions(
			cfg, profileStateResources, profileStateReaders, profileStatePlatforms,
		)
		if err != nil {
			return nil, fmt.Errorf("build profile state scheduled jobs: %w", err)
		}
		definitions = append(definitions, profileStateDefinitions...)
		resumeUpdateDefinitions, err := resumeUpdateDefinitions(
			cfg, profileStateResources, profileStateReaders, profileStateWriters, resumePublishers, profileStatePlatforms,
		)
		if err != nil {
			return nil, fmt.Errorf("build resume update scheduled jobs: %w", err)
		}
		definitions = append(definitions, resumeUpdateDefinitions...)
		// Campaign routes are registered once at startup; reuse the captured
		// definitions so a reload never disables the campaign schedules.
		definitions = append(definitions, campaignDefinitions...)
		return definitions, nil
	}
	definitions, err := buildReloadableDefinitions(cfg)
	if err != nil {
		log.Fatalf("build scheduled jobs: %v", err)
	}
	scheduler, err := jobscheduler.New(store, store, workflow.SystemClock{}, workflow.RandomIDGenerator{})
	if err != nil {
		log.Fatalf("create scheduler: %v", err)
	}
	scheduler.SetGate(newApplicationBudgetGate(cfg, instances, store))
	scheduler.SetPauseStore(store)
	if err := scheduler.Sync(context.Background(), definitions); err != nil {
		log.Fatalf("sync scheduled jobs: %v", err)
	}
	jobRunWorkflow, err := workflow.NewJobRunWorkflow(
		store, workflow.SystemClock{}, workflow.RandomIDGenerator{}, jobRunDefinitions(definitions),
	)
	if err != nil {
		log.Fatalf("create job run workflow: %v", err)
	}
	jobAPI, err := httpapi.NewJobAPI(jobRunWorkflow, store)
	if err != nil {
		log.Fatalf("create job API: %v", err)
	}
	jobAPI.SetDescriptions(configJobDescriptions(cfg))
	jobAPI.SetPauses(store)
	jobAPI.SetSystemTags(systemJobTags())
	runtimeAPI.ConfigureQuestionnaireCapture(vacancyTestWorkflow)
	profileDrafts, err := buildProfileDraftWorkflow(cfg, options.configPath, instances, store)
	if err != nil {
		log.Fatalf("create profile draft workflow: %v", err)
	}
	profileDraftAPI, err := httpapi.NewProfileDraftAPI(profileDrafts)
	if err != nil {
		log.Fatalf("create profile draft API: %v", err)
	}
	profileDraftAPI.ConfigureRestart(restartRequester{})
	live := &liveConfig{}
	live.Set(cfg)
	jobFragments, err := workflow.NewJobFragmentWorkflow(mustProfileStoreDirectory(cfg, options.configPath), func(raw json.RawMessage) error {
		var job appconfig.Job
		if err := json.Unmarshal(raw, &job); err != nil {
			return fmt.Errorf("decode job definition: %w", err)
		}
		for index := range job.Triggers {
			appconfig.NormalizeJobTrigger(&job.Triggers[index])
		}
		return live.Get().ValidateJobReplace(job)
	})
	if err != nil {
		log.Fatalf("create job fragment workflow: %v", err)
	}
	jobAPI.ConfigureEditor(jobFragments)
	authAPI, err := configureAuthAPI(cfg, instances, store, profileDrafts, authGuard)
	if err != nil {
		log.Fatalf("configure auth API: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	reloadStatus := &configReloadStatus{}
	runtimeAPI.ConfigureConfigStatus(reloadStatus.snapshot)
	refreshCampaigns := func(fresh appconfig.Config) error {
		routes, campaignDefinitionsFresh, _, _, err := campaignPlan(fresh, instances, profiles)
		if err != nil {
			return err
		}
		if err := campaignHandler.ReplaceRoutes(routes); err != nil {
			return err
		}
		campaignDefinitions = campaignDefinitionsFresh
		jobAPI.SetDescriptions(configJobDescriptions(fresh))
		logf("campaign routes reloaded: %d definitions", len(campaignDefinitionsFresh))
		return nil
	}
	// bindProfiles wires profiles that a fragment added to the running service:
	// the transports, readers and plans appear without a restart, while the
	// fields that workers read through plain maps still wait for the next
	// restart (see profileRuntimeBinder).
	boundProfiles := make(map[core.ProfileID]struct{}, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		boundProfiles[core.ProfileID(profile.Tag)] = struct{}{}
	}
	refreshProfileViews := func(fresh appconfig.Config) {
		runtimeAPI.SetProfiles(dashboardProfiles(fresh, profileContacts))
		runtimeAPI.SetStatePaths(profileStateFiles(fresh))
		profileCatalogAPI.SetEntries(profileCatalog(fresh, instances))
		resumeAPI.SetTargets(fresh.ResumeTargets())
		if authAPI != nil {
			authAPI.SetStatePaths(profileStateFiles(fresh))
		}
		profileDrafts.SetDeclared(configProfileTags(fresh))
	}
	bindProfiles := func(fresh appconfig.Config) error {
		live.Set(fresh)
		for _, profile := range fresh.Profiles {
			profileID := core.ProfileID(profile.Tag)
			if _, exists := boundProfiles[profileID]; exists {
				continue
			}
			runtimes, err := probeProfileAuthorizations(context.Background(), []appconfig.Profile{profile}, instances)
			if err != nil {
				return fmt.Errorf("probe profile %q: %w", profile.Tag, err)
			}
			for runtimeID, runtime := range runtimes {
				profiles[runtimeID] = runtime
			}
			contacts := resolveProfileContactsFor(profile, instances)
			profileContacts[profileID] = contacts
			preparer, err := applicationPreparer(profile, employerMatcher, applicationModels, contacts)
			if err != nil {
				return fmt.Errorf("build application operator for profile %q: %w", profile.Tag, err)
			}
			if profile.Enabled {
				if err := binder.bind(profile, preparer, true); err != nil {
					return fmt.Errorf("bind profile %q: %w", profile.Tag, err)
				}
				if instance := instances[profile.Adapter]; instance != nil && instance.Name() == hh.Name &&
					strings.TrimSpace(profile.StateFile) != "" {
					// The session refresher lives outside the binder: it keeps the
					// browser state fresh through the browser worker.
					if refreshClient, clientErr := browserWorkerHTTPClient(); clientErr == nil && refreshClient != nil {
						if err := sessionRefreshers.Register(profileID, refreshClient, profile.StateFile); err != nil {
							return fmt.Errorf("register session refresher for profile %q: %w", profile.Tag, err)
						}
					}
				}
			}
			boundProfiles[profileID] = struct{}{}
			logf("profile %q is bound without a restart", profile.Tag)
		}
		refreshProfileViews(fresh)
		return nil
	}
	applyJobDefinitions := func(definitions []jobscheduler.Definition) {
		if err := jobRunWorkflow.ReplaceDefinitions(jobRunDefinitions(definitions)); err != nil {
			logf("job definitions reload failed: %v", err)
			return
		}
		logf("runnable job definitions reloaded: %d", len(definitions))
	}
	go watchConfigReload(ctx, options.configPath, scheduler, bindProfiles, buildReloadableDefinitions, refreshCampaigns, applyJobDefinitions, reloadStatus)
	defer stop()
	instanceID, err := workflow.RandomIDGenerator{}.NewID("instance")
	if err != nil {
		log.Fatalf("generate runtime instance id: %v", err)
	}
	acquired, err := store.AcquireRuntimeInstance(ctx, instanceID, time.Now().UTC(), runtimeInstanceLeaseTTL)
	if err != nil {
		log.Fatalf("acquire runtime instance lease: %v", err)
	}
	if !acquired {
		log.Fatalf("another job-agent instance already holds the runtime lease; stop it before starting a second process")
	}
	defer func() {
		if err := store.ReleaseRuntimeInstance(context.Background(), instanceID); err != nil {
			logf("release runtime instance lease: %v", err)
		}
	}()
	go renewRuntimeInstance(ctx, store, instanceID, runtimeInstanceLeaseTTL)
	qualificationAPI, err := httpapi.NewQualificationAPI(store, qualificationWorkflow, qualificationPlatforms)
	if err != nil {
		log.Fatalf("create qualification API: %v", err)
	}
	var handler http.Handler = runtimeAPI.Handler(jobAPI.Handler(taskAPI.Handler(profileStateAPI.Handler(applicationAPI.Handler(resumeAPI.Handler(profileCatalogAPI.Handler(profileDraftAPI.Handler(qualificationAPI.Handler(reviewAPI.Handler(conversationAPI.Handler()))))))))))
	var authHandler func(http.Handler) http.Handler
	if authAPI != nil {
		authHandler = authAPI.Handler
	}
	handler = buildAPIHandler(
		handler, httpapi.NewMetricsAPI(store), apiToken, authHandler, slog.Default(),
	)
	// Every worker reports authorization failures to the same guard, so a lost
	// session stops the account's jobs and a later sign-in resumes them.
	for _, instance := range workers {
		instance.SetAuthGuard(authGuard.Guard)
	}
	if err := serve(ctx, cfg, handler, conversationWorkflow, scheduler, workers); err != nil {
		log.Fatal(err)
	}
}

// buildAPIHandler composes the public API chain. RequestID is outermost so the
// access log always carries the correlation ID; the bearer token wraps both the
// product API and the auth control plane, and gates /metrics as documented.
// Authenticated requests then pass the metrics counters and the short-lived
// response cache before routing.
func buildAPIHandler(
	product http.Handler,
	metrics *httpapi.MetricsAPI,
	apiToken string,
	authHandler func(http.Handler) http.Handler,
	logger *slog.Logger,
) http.Handler {
	handler := metrics.Handler(httpapi.NewResponseCache().Middleware(product))
	if authHandler != nil {
		handler = authHandler(handler)
	}
	if strings.TrimSpace(apiToken) != "" {
		handler = httpapi.BearerAuth(apiToken, handler)
	}
	handler = httpapi.AccessLog(logger, handler)
	return httpapi.RequestID(handler)
}

// restartRequester terminates this process after a short delay so the
// supervisor (docker compose with restart: unless-stopped, systemd and so on)
// starts it again with the new profile fragment. New profiles are bound at
// startup only, so onboarding ends with one restart.
type restartRequester struct{}

func (restartRequester) RequestRestart() {
	go func() {
		time.Sleep(2 * time.Second)
		logf("restart requested by profile onboarding: exiting for the supervisor")
		process, err := os.FindProcess(os.Getpid())
		if err != nil {
			logf("restart signal failed: %v", err)
			return
		}
		if err := process.Signal(syscall.SIGTERM); err != nil {
			logf("restart signal failed: %v", err)
		}
	}()
}

// buildProfileDraftWorkflow prepares the dashboard onboarding pipeline: it owns
// profile drafts, derives their session paths inside the profile store and
// writes the applied fragments there.
// liveConfig keeps the most recently loaded config: the dashboard job editor
// validates new definitions against the running state, not the startup one.
type liveConfig struct {
	mu  sync.RWMutex
	cfg appconfig.Config
}

func (holder *liveConfig) Set(cfg appconfig.Config) {
	holder.mu.Lock()
	holder.cfg = cfg
	holder.mu.Unlock()
}

func (holder *liveConfig) Get() appconfig.Config {
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return holder.cfg
}

// profileStoreDirectory resolves the fragment directory of the running config
// to an absolute path.
func profileStoreDirectory(cfg appconfig.Config, configPath string) (string, error) {
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	directory := cfg.ProfileStoreDirectory(filepath.Dir(absolute))
	if directory != "" {
		if resolved, err := filepath.Abs(directory); err == nil {
			directory = resolved
		}
	}
	return directory, nil
}

func buildProfileDraftWorkflow(cfg appconfig.Config, configPath string, instances map[string]adapter.Adapter, store *storesqlite.Store) (*workflow.ProfileDraftWorkflow, error) {
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}
	declared := make([]core.ProfileID, 0, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		declared = append(declared, core.ProfileID(profile.Tag))
	}
	platforms := make(map[string]string, len(cfg.Adapters))
	fallbackAdapter := ""
	for _, item := range cfg.Adapters {
		instance := instances[item.Tag]
		if instance == nil {
			continue
		}
		platforms[item.Tag] = instance.Name()
		if fallbackAdapter == "" && instance.Name() == hh.Name {
			fallbackAdapter = item.Tag
		}
	}
	reader := func(profileID core.ProfileID, stateFile string) (adapter.ProfileIdentityReader, error) {
		return hh.NewBrowserReadClient(profileID, stateFile, "", nil)
	}
	// The derived default can be process-relative; the runtime reports and
	// writes an absolute path so logs, the API and the fragments agree.
	directory := cfg.ProfileStoreDirectory(filepath.Dir(absolute))
	if directory != "" {
		if resolved, err := filepath.Abs(directory); err == nil {
			directory = resolved
		}
	}
	return workflow.NewProfileDraftWorkflow(
		store, declared, platforms, fallbackAdapter,
		directory, reader, workflow.SystemClock{},
	)
}

// draftLoginSettings lets the login driver start a session for a profile draft
// that is not declared in the config yet.
type draftLoginSettings struct {
	drafts *workflow.ProfileDraftWorkflow
}

func (resolver draftLoginSettings) LoginSettings(profileID core.ProfileID) (hh.LoginSettings, bool) {
	if resolver.drafts == nil {
		return hh.LoginSettings{}, false
	}
	stateFile, ok := resolver.drafts.ProfileStateFile(context.Background(), profileID)
	if !ok {
		return hh.LoginSettings{}, false
	}
	return hh.LoginSettings{ProfileID: profileID, StateFile: stateFile}, true
}

// configureAuthAPI wires the interactive login control plane when the browser
// worker is configured. The credential writer runs with Force enabled because
// the CLI enforces the explicit --force decision before creating a session.
// authCompletionHooks runs every post-login observer: the draft identity
// capture, the account summary of a config profile, and the recovery of jobs
// and tasks that failed without a session.
type authCompletionHooks struct {
	drafts   *workflow.ProfileDraftWorkflow
	guard    *workflow.AuthGuard
	identity profileIdentityCapture
}

func (hooks authCompletionHooks) AuthCompleted(ctx context.Context, profileID core.ProfileID, browserStateReference string) error {
	if hooks.drafts != nil {
		if err := hooks.drafts.AuthCompleted(ctx, profileID, browserStateReference); err != nil {
			logf("profile draft capture failed for %s: %v", profileID, err)
		}
	}
	if err := hooks.identity.capture(ctx, profileID, browserStateReference); err != nil {
		logf("profile identity capture failed for %s: %v", profileID, err)
	}
	if hooks.guard != nil {
		return hooks.guard.AuthCompleted(ctx, profileID, browserStateReference)
	}
	return nil
}

// profileIdentityCapture stores the account summary of a profile that lives in
// the read-only config file: the dashboard then shows the account without the
// operator editing the config.
type profileIdentityCapture struct {
	reader func(profileID core.ProfileID, stateFile string) (adapter.ProfileIdentityReader, error)
	store  interface {
		SaveProfileIdentity(ctx context.Context, profileID core.ProfileID, identity core.ProfileIdentity, now time.Time) error
	}
	clock workflow.Clock
}

func (capture profileIdentityCapture) capture(ctx context.Context, profileID core.ProfileID, stateFile string) error {
	if capture.reader == nil || capture.store == nil || strings.TrimSpace(stateFile) == "" {
		return nil
	}
	reader, err := capture.reader(profileID, stateFile)
	if err != nil {
		return err
	}
	snapshot, err := reader.ReadProfileIdentity(ctx, profileID)
	if err != nil {
		return err
	}
	identity := core.ProfileIdentity{
		DisplayName: strings.TrimSpace(snapshot.DisplayName),
		Email:       strings.TrimSpace(snapshot.Email),
		Phone:       strings.TrimSpace(snapshot.Phone),
		AccountHash: strings.TrimSpace(snapshot.AccountHash),
		CapturedAt:  snapshot.CapturedAt,
	}
	if identity.DisplayName == "" && identity.Email == "" && identity.Phone == "" && identity.AccountHash == "" {
		return nil
	}
	return capture.store.SaveProfileIdentity(ctx, profileID, identity, capture.clock.Now())
}

func configureAuthAPI(cfg appconfig.Config, instances map[string]adapter.Adapter, store *storesqlite.Store, drafts *workflow.ProfileDraftWorkflow, guard *workflow.AuthGuard) (*httpapi.AuthAPI, error) {
	baseURL := strings.TrimSpace(os.Getenv("BROWSER_WORKER_URL"))
	token := strings.TrimSpace(os.Getenv("BROWSER_WORKER_TOKEN"))
	if baseURL == "" || token == "" {
		logf("browser worker is not configured; interactive auth API is disabled")
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
		logf("browser worker is configured but no HH profile has a state file yet; only profile drafts can log in")
	}
	driver, err := hh.NewLoginDriver(client, settings)
	if err != nil {
		return nil, err
	}
	driver.SetProfileResolver(draftLoginSettings{drafts: drafts})
	service, err := auth.NewService(
		store, auth.NewMemoryChallengeStore(), driver,
		&auth.FileCredentialWriter{Force: true}, &auth.FileBrowserStateWriter{},
		workflow.SystemClock{}, workflow.RandomIDGenerator{},
	)
	if err != nil {
		return nil, err
	}
	service.SetCompletionHook(authCompletionHooks{
		drafts: drafts, guard: guard,
		identity: profileIdentityCapture{
			reader: func(profileID core.ProfileID, stateFile string) (adapter.ProfileIdentityReader, error) {
				return hh.NewBrowserReadClient(profileID, stateFile, "", nil)
			},
			store: store, clock: workflow.SystemClock{},
		},
	})
	authAPI, err := httpapi.NewAuthAPI(service, profileStateFiles(cfg))
	if err != nil {
		return nil, err
	}
	authAPI.ConfigureStateResolver(drafts)
	logoutTargets := make(map[core.ProfileID]auth.LogoutTarget)
	for _, profile := range cfg.Profiles {
		if strings.TrimSpace(profile.CredentialsRef) == "" && strings.TrimSpace(profile.StateFile) == "" {
			continue
		}
		logoutTargets[core.ProfileID(profile.Tag)] = auth.LogoutTarget{
			ProfileID:             core.ProfileID(profile.Tag),
			CredentialReference:   profile.CredentialsRef,
			BrowserStateReference: profile.StateFile,
		}
	}
	authAPI.ConfigureLogout(&auth.LogoutService{}, logoutTargets)
	return authAPI, nil
}

type profileBudgetLimit struct {
	limit    int
	timezone string
}

// applicationBudgetGate pauses scheduled campaigns when every profile they
// target has spent its daily application budget. Profiles without a submit
// mode or without a configured limit never block the run; skipped occurrences
// simply wait for the next cron time, when the budget window may have reset.
type applicationBudgetGate struct {
	budgets *storesqlite.Store
	limits  map[core.ProfileID]profileBudgetLimit
	clock   workflow.Clock
}

func newApplicationBudgetGate(cfg appconfig.Config, instances map[string]adapter.Adapter, budgets *storesqlite.Store) applicationBudgetGate {
	limits := make(map[core.ProfileID]profileBudgetLimit)
	for _, profile := range cfg.Profiles {
		if !profile.Enabled || profile.Applications.ExecutionMode() != appconfig.ApplicationModeSubmit {
			continue
		}
		instance := instances[profile.Adapter]
		limits[core.ProfileID(profile.Tag)] = profileBudgetLimit{
			limit:    profile.Applications.EffectiveDailyLimit(instance.Name()),
			timezone: profile.Applications.LocationName(),
		}
	}
	return applicationBudgetGate{budgets: budgets, limits: limits, clock: workflow.SystemClock{}}
}

func (gate applicationBudgetGate) Allow(ctx context.Context, definition jobscheduler.Definition) (bool, error) {
	if definition.ActionType != core.TaskApplicationCampaign {
		return true, nil
	}
	var payload core.ApplicationCampaignPayload
	if err := json.Unmarshal(definition.Payload, &payload); err != nil {
		return false, fmt.Errorf("decode campaign payload for budget gate: %w", err)
	}
	if len(payload.Profiles) == 0 {
		return true, nil
	}
	now := gate.clock.Now()
	for _, profileID := range payload.Profiles {
		budget, configured := gate.limits[profileID]
		if !configured || budget.limit <= 0 {
			return true, nil
		}
		windowStart, _, err := taskworker.ApplicationBudgetWindow(budget.timezone, now)
		if err != nil {
			return false, fmt.Errorf("resolve budget window for profile %s: %w", profileID, err)
		}
		usage, err := gate.budgets.ApplicationBudgetUsage(ctx, profileID, definition.Platform, windowStart)
		if err != nil {
			return false, fmt.Errorf("read budget usage for profile %s: %w", profileID, err)
		}
		if !usage.Exhausted() {
			return true, nil
		}
	}
	return false, nil
}

// triggerIndexForProfile keeps scheduler rows unique when one job expands
// into several profiles: the profile index is folded into the trigger index.
func triggerIndexForProfile(triggerIndex, profileIndex, triggerCount int) int {
	return profileIndex*triggerCount + triggerIndex
}

func jobRunDefinitions(definitions []jobscheduler.Definition) []workflow.JobRunDefinition {
	positions := make(map[string]int, len(definitions))
	result := make([]workflow.JobRunDefinition, 0, len(definitions))
	seenCommands := make(map[string]map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		position, exists := positions[definition.JobTag]
		if !exists {
			position = len(result)
			positions[definition.JobTag] = position
			result = append(result, workflow.JobRunDefinition{Tag: definition.JobTag})
			seenCommands[definition.JobTag] = make(map[string]struct{})
		}
		commandKey := string(definition.ProfileID) + "\x00" + string(definition.Payload)
		if _, duplicate := seenCommands[definition.JobTag][commandKey]; duplicate {
			continue
		}
		seenCommands[definition.JobTag][commandKey] = struct{}{}
		result[position].Commands = append(result[position].Commands, workflow.JobRunCommand{
			TaskType: definition.ActionType, Platform: definition.Platform,
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
			logf("profile bootstrap for %q is disabled with the profile", profile.Tag)
			continue
		}
		if profiles[profileID].Status == core.ProfileAuthRequired {
			logf("profile bootstrap for %q is waiting for authentication", profile.Tag)
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
			logf("profile bootstrap for %q skipped: at least one declared field is already populated", profile.Tag)
		case result.Proposal.Status == core.ProfileStateProposalNoChanges:
			logf("profile bootstrap for %q has no changes", profile.Tag)
		case result.TaskCreated:
			logf("profile bootstrap for %q queued apply task %q", profile.Tag, result.Task.ID)
		default:
			logf("profile bootstrap for %q reused apply task %q in status %q", profile.Tag, result.Task.ID, result.Task.Status)
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

// runtimeInstanceLeaseTTL bounds how long a crashed process can block the
// database. The single-replica guard refuses a second live instance; scaling
// beyond one replica needs shared coordination such as PostgreSQL or Redis.
const runtimeInstanceLeaseTTL = 90 * time.Second

func renewRuntimeInstance(ctx context.Context, store *storesqlite.Store, owner string, ttl time.Duration) {
	ticker := time.NewTicker(ttl / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewed, err := store.RenewRuntimeInstance(ctx, owner, time.Now().UTC())
			if err != nil {
				logf("renew runtime instance lease: %v", err)
				continue
			}
			if !renewed {
				logf("runtime instance lease is no longer owned by this process")
				return
			}
		}
	}
}

// logf keeps existing operational messages while emitting them through the
// default structured logger.
func logf(format string, args ...any) {
	slog.Info(fmt.Sprintf(format, args...))
}

// resolveAPIToken reads the configured bearer token environment variable. An
// empty name keeps the API unprotected, which is only valid together with a
// loopback bind.
func resolveAPIToken(envName string, lookupEnv func(string) (string, bool)) (string, error) {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return "", nil
	}
	if lookupEnv == nil {
		return "", errors.New("api token resolution requires lookup function")
	}
	value, exists := lookupEnv(envName)
	if !exists || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("server api token environment variable %s is not set", envName)
	}
	return value, nil
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

// resolveProfileContacts reads the sender name and contacts from the platform
// profile once at startup, then lets the optional config block fill the gaps.
// The platform stays the source of truth for the name; config remains a
// fallback for profiles without a trusted reader.
func resolveProfileContacts(cfg appconfig.Config, instances map[string]adapter.Adapter) map[core.ProfileID]applicationoperator.ApplicationProfileContext {
	resolved := make(map[core.ProfileID]applicationoperator.ApplicationProfileContext, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		if !profile.Enabled {
			continue
		}
		resolved[core.ProfileID(profile.Tag)] = resolveProfileContactsFor(profile, instances)
	}
	return resolved
}

// resolveProfileContactsFor reads the sender name and contacts of one profile
// from the platform once, then lets the optional config block fill the gaps.
func resolveProfileContactsFor(profile appconfig.Profile, instances map[string]adapter.Adapter) applicationoperator.ApplicationProfileContext {
	profileID := core.ProfileID(profile.Tag)
	fallback := configProfileContacts(profile)
	if !profile.Enabled {
		return fallback
	}
	if strings.TrimSpace(profile.Resume) == "" || strings.TrimSpace(profile.StateFile) == "" {
		return fallback
	}
	instance := instances[profile.Adapter]
	if instance == nil {
		return fallback
	}
	if binder, ok := instance.(adapter.BrowserSessionBinder); ok {
		if _, err := binder.BindBrowserSession(profileID, profile.StateFile); err != nil {
			logf("profile %q contacts: browser session failed, using config: %v", profile.Tag, err)
			return fallback
		}
	}
	reader, ok := instance.(adapter.ProfileStateReader)
	if !ok {
		return fallback
	}
	observation, err := reader.ReadProfileState(context.Background(), adapter.ProfileStateReadRequest{
		ProfileID: profileID, Paths: profileContactPaths(profile.Resume),
	})
	if err != nil {
		logf("profile %q contacts: platform read failed, using config: %v", profile.Tag, err)
		return fallback
	}
	contacts := profileContactsFromObservation(observation, profile.Resume)
	if contacts.FirstName == "" {
		contacts.FirstName = fallback.FirstName
	}
	if contacts.LastName == "" {
		contacts.LastName = fallback.LastName
	}
	if contacts.Email == "" {
		contacts.Email = fallback.Email
	}
	if contacts.Telegram == "" {
		contacts.Telegram = fallback.Telegram
	}
	logf("profile %q contacts resolved: %s", profile.Tag, contactFieldNames(contacts))
	return contacts
}

func configProfileContacts(profile appconfig.Profile) applicationoperator.ApplicationProfileContext {
	if profile.Contacts == nil {
		return applicationoperator.ApplicationProfileContext{}
	}
	return applicationoperator.ApplicationProfileContext{
		FirstName: profile.Contacts.FirstName, LastName: profile.Contacts.LastName,
		Email: profile.Contacts.Email, Telegram: profile.Contacts.Telegram,
	}
}

func profileContactPaths(resumeID string) []string {
	return hh.ProfileContactPaths(resumeID)
}

// profileContactsFromObservation maps the HH profile document onto the letter
// context; the HH field layout lives in the adapter.
func profileContactsFromObservation(observation core.ProfileStateObservation, resumeID string) applicationoperator.ApplicationProfileContext {
	contacts := hh.ContactsFromProfileState(observation, resumeID)
	return applicationoperator.ApplicationProfileContext{
		FirstName: contacts.FirstName,
		LastName:  contacts.LastName,
		Email:     contacts.Email,
		Telegram:  contacts.Telegram,
	}
}

func contactFieldNames(contacts applicationoperator.ApplicationProfileContext) string {
	names := make([]string, 0, 4)
	for name, value := range map[string]string{
		"first_name": contacts.FirstName, "last_name": contacts.LastName,
		"email": contacts.Email, "telegram": contacts.Telegram,
	} {
		if strings.TrimSpace(value) != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

func applicationPreparer(profile appconfig.Profile, employerMatcher *applicationoperator.EmployerGroupMatcher, models map[string]applicationoperator.ApplicationMessageModel, contacts applicationoperator.ApplicationProfileContext) (applicationoperator.ApplicationPreparer, error) {
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
		IncludeAll:      profile.Applications.Qualification.IncludeAll,
		ExcludeAny:      profile.Applications.Qualification.ExcludeAny,
		ExcludeAll:      profile.Applications.Qualification.ExcludeAll,
		StaticMessage:   profile.Applications.Message,
		MessageTemplate: messageTemplate,
		MessagePool:     messagePool,
		Model:           model,
		Resume:          resumeContext,
		Profile:         contacts,
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
		case appconfig.ModelProviderOpenAIChat:
			model, err = openaichat.New(openaichat.Config{
				BaseURL: configured.BaseURL, APIKey: apiKey, Model: configured.Model,
				MaxOutputTokens: configured.MaxOutputTokens, ReasoningEffort: configured.ReasoningEffort,
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

func answerModel(configured *appconfig.AnswerModelPolicy, models map[string]applicationoperator.ApplicationMessageModel) (*applicationoperator.AnswerModel, error) {
	if configured == nil {
		return nil, nil
	}
	provider := strings.TrimSpace(configured.Provider)
	generator, ok := models[provider].(applicationoperator.AnswerGenerator)
	if !ok || generator == nil {
		return nil, fmt.Errorf("answer model references unavailable provider %q", provider)
	}
	timeout, err := configured.TimeoutDuration()
	if err != nil || timeout <= 0 {
		return nil, fmt.Errorf("answer model provider %q has invalid timeout %q", provider, configured.Timeout)
	}
	return applicationoperator.CompileAnswerModel(&applicationoperator.AnswerModelConfig{
		Tag: provider, PromptVersion: configured.PromptVersion,
		Instruction: configured.Instruction, Timeout: timeout, Generator: generator,
	})
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

func tailoringReadObjects(processor string, readonly []string) ([]applicationoperator.ResumeTailoringObjectReference, error) {
	_, reads, err := resumeTailoringObjectRefs(processor, nil, readonly)
	return reads, err
}

func resumeTailoringObjectRefs(processor string, write, readonly []string) ([]applicationoperator.ResumeTailoringObjectReference, []applicationoperator.ResumeTailoringObjectReference, error) {
	writes := make([]applicationoperator.ResumeTailoringObjectReference, 0, len(write))
	for _, value := range write {
		ref, err := applicationoperator.ParseResumeTailoringObject(value)
		if err != nil {
			return nil, nil, err
		}
		if err := applicationoperator.ValidateResumeTailoringWriteObject(processor, ref); err != nil {
			return nil, nil, err
		}
		writes = append(writes, ref)
	}
	reads := make([]applicationoperator.ResumeTailoringObjectReference, 0, len(readonly))
	for _, value := range readonly {
		ref, err := applicationoperator.ParseResumeTailoringObject(value)
		if err != nil {
			return nil, nil, err
		}
		if err := applicationoperator.ValidateResumeTailoringReadObject(ref); err != nil {
			return nil, nil, err
		}
		reads = append(reads, ref)
	}
	return writes, reads, nil
}

func appendTailoringObjectPaths(paths []string, resumeID string, refs ...applicationoperator.ResumeTailoringObjectReference) []string {
	for _, ref := range refs {
		path := applicationoperator.ResumeTailoringObjectPath(resumeID, ref)
		if !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	return paths
}

func applicationTailoringProcessor(profile appconfig.Profile, models map[string]applicationoperator.ApplicationMessageModel, contacts applicationoperator.ApplicationProfileContext) (applicationoperator.ResumeTailoringProcessor, []string, error) {
	processors := make([]applicationoperator.ResumeTailoringProcessor, 0, 2)
	allowedPaths := make([]string, 0, 2)
	skills, skillsEnabled := profile.Applications.TailoringSkills()
	if skillsEnabled {
		skillsWrite, skillsRead, err := resumeTailoringObjectRefs("skills", skills.Write, skills.Readonly)
		if err != nil {
			return nil, nil, err
		}
		if len(skillsWrite) == 0 {
			skillsWrite = []applicationoperator.ResumeTailoringObjectReference{{Name: "keySkills"}}
		}
		_ = skillsRead
		deterministic, err := applicationoperator.NewAddVacancySkillsProcessor("skills-from-vacancy", "v1", skills.Maximum)
		if err != nil {
			return nil, nil, err
		}
		var skillProcessor applicationoperator.ResumeTailoringProcessor = deterministic
		if skills.Model != nil {
			provider := strings.TrimSpace(skills.Model.Provider)
			candidate := models[provider]
			if candidate == nil {
				return nil, nil, fmt.Errorf("application tailoring model references unavailable provider %q", provider)
			}
			tailoringModel, ok := candidate.(applicationoperator.ResumeTailoringModel)
			if !ok {
				return nil, nil, fmt.Errorf("application tailoring model provider %q does not support skill selection", provider)
			}
			timeout, err := time.ParseDuration(skills.Model.Timeout)
			if err != nil || timeout <= 0 {
				return nil, nil, fmt.Errorf("application tailoring model provider %q has invalid timeout %q", provider, skills.Model.Timeout)
			}
			skillProcessor, err = applicationoperator.NewModelResumeTailoringProcessor(applicationoperator.ModelResumeTailoringConfig{
				Tag: provider, PromptVersion: skills.Model.PromptVersion, Instruction: skills.Model.Instruction,
				MaximumSkills: skills.Maximum, AllowRemovals: skills.AllowRemovals,
				Timeout: timeout, Model: tailoringModel, Fallback: deterministic,
			})
			if err != nil {
				return nil, nil, err
			}
		}
		processors = append(processors, skillProcessor)
		allowedPaths = append(allowedPaths, applicationoperator.ResumeSkillsPath(profile.Resume))
		allowedPaths = appendTailoringObjectPaths(allowedPaths, profile.Resume, skillsWrite...)
		allowedPaths = appendTailoringObjectPaths(allowedPaths, profile.Resume, skillsRead...)
	}
	if about, enabled := profile.Applications.TailoringAbout(); enabled {
		var resumeFacts *applicationoperator.ApplicationResumeContext
		if facts, exists := profile.ResolvedResumeFacts(); exists {
			resumeFacts = &applicationoperator.ApplicationResumeContext{
				ResumeID: facts.ResumeID, FactsTag: facts.Tag, Digest: facts.Digest, Facts: facts.Facts,
			}
		}
		provider := strings.TrimSpace(about.Model.Provider)
		candidate := models[provider]
		if candidate == nil {
			return nil, nil, fmt.Errorf("application tailoring about references unavailable provider %q", provider)
		}
		aboutModel, ok := candidate.(applicationoperator.ResumeTailoringAboutModel)
		if !ok {
			return nil, nil, fmt.Errorf("application tailoring model provider %q does not support about rewrite", provider)
		}
		timeout, err := time.ParseDuration(about.Model.Timeout)
		if err != nil || timeout <= 0 {
			return nil, nil, fmt.Errorf("application tailoring about provider %q has invalid timeout %q", provider, about.Model.Timeout)
		}
		aboutRead, readErr := tailoringReadObjects("about", about.Readonly)
		if readErr != nil {
			return nil, nil, readErr
		}
		aboutProcessor, err := applicationoperator.NewModelResumeTailoringAboutProcessor(applicationoperator.ModelResumeTailoringAboutConfig{
			Tag: provider, PromptVersion: about.Model.PromptVersion, Instruction: about.Model.Instruction,
			MaximumRunes: about.MaximumRunes, Timeout: timeout, Model: aboutModel, Facts: resumeFacts,
			Contacts: contacts, ContextObjects: aboutRead,
		})
		if err != nil {
			return nil, nil, err
		}
		processors = append(processors, aboutProcessor)
		for _, path := range applicationoperator.ResumeTailoringAboutReadPaths(profile.Resume) {
			if !slices.Contains(allowedPaths, path) {
				allowedPaths = append(allowedPaths, path)
			}
		}
		allowedPaths = appendTailoringObjectPaths(allowedPaths, profile.Resume, aboutRead...)
	}
	if experience, enabled := profile.Applications.TailoringExperience(); enabled {
		configuredGroups := experience.Groups
		type groupModelOutcome struct {
			model applicationoperator.ResumeTailoringExperienceModel
		}
		_ = groupModelOutcome{}
		buildGroup := func(modelPolicy *appconfig.ApplicationModelPolicy, write, readonly []string, maximumRunes int) (applicationoperator.ResumeTailoringExperienceGroupConfig, []applicationoperator.ResumeTailoringObjectReference, []applicationoperator.ResumeTailoringObjectReference, error) {
			provider := strings.TrimSpace(modelPolicy.Provider)
			candidate := models[provider]
			if candidate == nil {
				return applicationoperator.ResumeTailoringExperienceGroupConfig{}, nil, nil, fmt.Errorf("application tailoring experience references unavailable provider %q", provider)
			}
			if _, ok := candidate.(applicationoperator.ResumeTailoringExperienceModel); !ok {
				return applicationoperator.ResumeTailoringExperienceGroupConfig{}, nil, nil, fmt.Errorf("application tailoring model provider %q does not support experience rewrite", provider)
			}
			timeout, err := time.ParseDuration(modelPolicy.Timeout)
			if err != nil || timeout <= 0 {
				return applicationoperator.ResumeTailoringExperienceGroupConfig{}, nil, nil, fmt.Errorf("application tailoring experience provider %q has invalid timeout %q", provider, modelPolicy.Timeout)
			}
			_ = timeout
			writeRefs, _, err := resumeTailoringObjectRefs("experience", write, readonly)
			if err != nil {
				return applicationoperator.ResumeTailoringExperienceGroupConfig{}, nil, nil, err
			}
			allowReorder := false
			targets := make([]applicationoperator.ResumeTailoringObjectReference, 0, len(writeRefs))
			for _, ref := range writeRefs {
				if ref.Field == "order" {
					allowReorder = true
					continue
				}
				targets = append(targets, ref)
			}
			contextRefs, err := tailoringReadObjects("experience", readonly)
			if err != nil {
				return applicationoperator.ResumeTailoringExperienceGroupConfig{}, nil, nil, err
			}
			return applicationoperator.ResumeTailoringExperienceGroupConfig{
				Instruction: modelPolicy.Instruction, Targets: targets, ContextObjects: contextRefs,
				AllowReorder: allowReorder, MaximumRunes: maximumRunes,
			}, writeRefs, contextRefs, nil
		}
		groupConfigs := make([]applicationoperator.ResumeTailoringExperienceGroupConfig, 0, len(configuredGroups))
		allWriteRefs := make([]applicationoperator.ResumeTailoringObjectReference, 0)
		allContextRefs := make([]applicationoperator.ResumeTailoringObjectReference, 0)
		if len(configuredGroups) > 0 {
			for _, group := range configuredGroups {
				groupConfig, writeRefs, contextRefs, err := buildGroup(group.Model, group.Write, group.Readonly, group.MaximumRunes)
				if err != nil {
					return nil, nil, err
				}
				groupConfigs = append(groupConfigs, groupConfig)
				allWriteRefs = append(allWriteRefs, writeRefs...)
				allContextRefs = append(allContextRefs, contextRefs...)
			}
		} else {
			groupConfig, writeRefs, contextRefs, err := buildGroup(experience.Model, experience.Write, experience.Readonly, experience.MaximumRunes)
			if err != nil {
				return nil, nil, err
			}
			groupConfigs = append(groupConfigs, groupConfig)
			allWriteRefs = append(allWriteRefs, writeRefs...)
			allContextRefs = append(allContextRefs, contextRefs...)
		}
		first := configuredGroups[0]
		_ = first
		providerModel := func() applicationoperator.ResumeTailoringExperienceModel {
			policy := experience.Model
			if len(configuredGroups) > 0 {
				policy = configuredGroups[0].Model
			}
			candidate := models[strings.TrimSpace(policy.Provider)]
			model, _ := candidate.(applicationoperator.ResumeTailoringExperienceModel)
			return model
		}()
		timeout := 60 * time.Second
		{
			policy := experience.Model
			if len(configuredGroups) > 0 {
				policy = configuredGroups[0].Model
			}
			if parsed, err := time.ParseDuration(policy.Timeout); err == nil && parsed > 0 {
				timeout = parsed
			}
		}
		experienceProcessor, err := applicationoperator.NewModelResumeTailoringExperienceProcessor(applicationoperator.ModelResumeTailoringExperienceConfig{
			Tag: "experience-tailoring", PromptVersion: "v1", MaximumRunes: experience.MaximumRunes,
			Timeout: timeout, Model: providerModel, Groups: groupConfigs,
		})
		if err != nil {
			return nil, nil, err
		}
		processors = append(processors, experienceProcessor)
		allowedPaths = append(allowedPaths, applicationoperator.ResumeExperiencePath(profile.Resume))
		for _, path := range applicationoperator.ResumeTailoringExperienceReadPaths(profile.Resume) {
			if !slices.Contains(allowedPaths, path) {
				allowedPaths = append(allowedPaths, path)
			}
		}
		allowedPaths = appendTailoringObjectPaths(allowedPaths, profile.Resume, allWriteRefs...)
		allowedPaths = appendTailoringObjectPaths(allowedPaths, profile.Resume, allContextRefs...)
	}
	switch len(processors) {
	case 0:
		return nil, nil, nil
	case 1:
		return processors[0], allowedPaths, nil
	default:
		chain, err := applicationoperator.NewChainResumeTailoringProcessor(applicationoperator.ChainResumeTailoringConfig{
			Tag: "application-tailoring", Version: "v1", Processors: processors,
		})
		if err != nil {
			return nil, nil, err
		}
		return chain, allowedPaths, nil
	}
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
			logf("search %q is disabled until one of its profiles is authorized", search.Tag)
			continue
		}
		if checker, ok := instance.(adapter.SearchSupportChecker); ok {
			if err := checker.SupportsSearch(searchProfileID, search.Query); err != nil {
				logf("search %q is skipped for profile %q: %v", search.Tag, searchProfileID, err)
				continue
			}
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
		created, err := handler.EnsureRun(ctx, run, search.Priority)
		if err != nil {
			return nil, 0, fmt.Errorf("ensure search run %q: %w", search.Tag, err)
		}
		if created {
			if stored, readErr := store.SearchRun(ctx, searchID); readErr == nil && stored.Generation > 1 {
				logf("search %q configuration changed; generation %d starts with an empty cursor", search.Tag, stored.Generation)
			}
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
	handler, err := workflow.NewApplicationCampaignHandler(
		store, store, store, store, workflow.SystemClock{}, workflow.RandomIDGenerator{}, 5*time.Second,
	)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	routes, definitions, ownedRoutes, configuredJobs, err := campaignPlan(cfg, instances, profiles)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	if err := handler.ReplaceRoutes(routes); err != nil {
		return nil, nil, nil, 0, err
	}
	return handler, definitions, ownedRoutes, configuredJobs, nil
}

// campaignPlan builds the route set and cron definitions for the configured
// campaign jobs. The same plan is reused by a configuration reload.
func campaignPlan(cfg appconfig.Config, instances map[string]adapter.Adapter, profiles map[core.ProfileID]profileRuntime) ([]workflow.ApplicationCampaignRoute, []jobscheduler.Definition, map[core.SearchID]struct{}, int, error) {
	searches := make(map[string]appconfig.Search, len(cfg.Searches))
	for _, search := range cfg.Searches {
		searches[search.Tag] = search
	}
	configuredProfiles := make(map[core.ProfileID]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		configuredProfiles[core.ProfileID(profile.Tag)] = profile
	}
	routes := make([]workflow.ApplicationCampaignRoute, 0)
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
				logf("application campaign %q is disabled until profile %q has read access", job.Tag, value)
				runnable = false
			}
		}
		expandedRoutes := make([]string, 0, len(job.Action.Routes))
		seenRoutes := make(map[string]struct{}, len(job.Action.Routes))
		for _, value := range job.Action.Routes {
			chain, err := cfg.ResolveSearchChain(value)
			if err != nil {
				return nil, nil, nil, configuredJobs, fmt.Errorf("job %q: %w", job.Tag, err)
			}
			for _, chainTag := range chain {
				if _, exists := seenRoutes[chainTag]; exists {
					continue
				}
				seenRoutes[chainTag] = struct{}{}
				expandedRoutes = append(expandedRoutes, chainTag)
			}
		}
		var platform core.Platform
		jobRouteIDs := make([]core.SearchID, 0, len(expandedRoutes))
		for _, value := range expandedRoutes {
			search := searches[value]
			instance := instances[search.Adapter]
			if platform == "" {
				platform = core.Platform(instance.Name())
			}
			// A resume placeholder expands the route into one search per resume
			// of every capable profile, so one search object serves several
			// accounts without repetition in the config. A concrete resume (and
			// every other source) keeps a single route under the first profile
			// with read access.
			type routeVariant struct {
				profileID core.ProfileID
				query     json.RawMessage
			}
			variants := make([]routeVariant, 0, len(search.Profiles))
			if hasResumePlaceholder(search.Query) {
				for _, profile := range search.Profiles {
					profileID := core.ProfileID(profile)
					if !profiles[profileID].canReadVacancies() {
						continue
					}
					resolved, err := resumeQueryVariants(search.Query, configuredProfiles[profileID])
					if err != nil {
						return nil, nil, nil, configuredJobs, fmt.Errorf("job %q route %q: %w", job.Tag, search.Tag, err)
					}
					for _, query := range resolved {
						variants = append(variants, routeVariant{profileID: profileID, query: query})
					}
				}
			} else {
				for _, profile := range search.Profiles {
					profileID := core.ProfileID(profile)
					if profiles[profileID].canReadVacancies() {
						variants = append(variants, routeVariant{profileID: profileID, query: search.Query})
						break
					}
				}
			}
			if len(variants) == 0 {
				logf("application campaign route %q is disabled until one of its profiles has read access", search.Tag)
				runnable = false
				continue
			}
			for index, variant := range variants {
				routeID := core.SearchID(search.Tag)
				if index > 0 {
					routeID = core.SearchID(fmt.Sprintf("%s#%d", search.Tag, index+1))
				}
				jobRouteIDs = append(jobRouteIDs, routeID)
				if _, exists := registered[routeID]; exists {
					continue
				}
				routes = append(routes, workflow.ApplicationCampaignRoute{
					SearchID: routeID, Platform: core.Platform(instance.Name()), SearchProfileID: variant.profileID,
					Query: variant.query, Searcher: instance,
				})
				registered[routeID] = struct{}{}
			}
		}
		if !runnable {
			continue
		}
		profileIDs := make([]core.ProfileID, 0, len(job.Action.Profiles))
		for _, value := range job.Action.Profiles {
			profileIDs = append(profileIDs, core.ProfileID(value))
		}
		routeIDs := make([]core.SearchID, 0, len(jobRouteIDs))
		for _, routeID := range jobRouteIDs {
			routeIDs = append(routeIDs, routeID)
			ownedRoutes[routeID] = struct{}{}
		}
		// The daily limit caps the target per profile: one account's limit must not
		// lower another account's campaign.
		targets := make(map[core.ProfileID]int, len(profileIDs))
		for _, value := range job.Action.Profiles {
			var configured appconfig.Profile
			for _, candidate := range cfg.Profiles {
				if candidate.Tag == value {
					configured = candidate
					break
				}
			}
			instance := instances[configured.Adapter]
			if configured.Tag == "" || instance == nil {
				continue
			}
			target := job.Action.TargetSuccessful
			if limit := configured.Applications.EffectiveDailyLimit(instance.Name()); limit > 0 && target > limit {
				logf("campaign %q target %d is capped to the daily limit %d of profile %q",
					job.Tag, target, limit, configured.Tag)
				target = limit
			}
			targets[core.ProfileID(configured.Tag)] = target
		}
		// One schedule per profile, like every other multi-profile job: each
		// account gets its own campaign run, pause and budget gate, so the
		// dashboard can count and control profiles separately.
		for profileIndex, profileID := range profileIDs {
			target := job.Action.TargetSuccessful
			if capped, exists := targets[profileID]; exists {
				target = capped
			}
			payload, err := json.Marshal(core.NewApplicationCampaignStartPayload(
				job.Tag, []core.ProfileID{profileID}, routeIDs, target, job.Action.MaxInFlight,
			))
			if err != nil {
				return nil, nil, nil, configuredJobs, fmt.Errorf("encode application campaign %q for profile %q: %w", job.Tag, profileID, err)
			}
			for index, trigger := range job.Triggers {
				minimum, maximum := trigger.Jitter.Durations()
				definitions = append(definitions, jobscheduler.Definition{
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskApplicationCampaign, Platform: platform, ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
		}
		configuredJobs++
	}
	return routes, definitions, ownedRoutes, configuredJobs, nil
}

// mustProfileStoreDirectory resolves the fragment directory or fails fast at
// startup: a missing directory simply means no fragments yet.
func mustProfileStoreDirectory(cfg appconfig.Config, configPath string) string {
	directory, err := profileStoreDirectory(cfg, configPath)
	if err != nil {
		log.Fatalf("resolve profile store directory: %v", err)
	}
	return directory
}

// configProfileTags lists the declared profile tags of a config.
func configProfileTags(cfg appconfig.Config) []core.ProfileID {
	tags := make([]core.ProfileID, 0, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		tags = append(tags, core.ProfileID(profile.Tag))
	}
	return tags
}

// activityMaintainQuery returns the vacancy source of the activity maintenance
// job: the operator's override when the profile declares one, otherwise a
// filterless global search. The job only needs vacancies to open, not the
// profile's narrow job search, so the default keeps a practically endless pool
// of fresh cards.
var activityMaintainDefaultQuery = json.RawMessage(`{"source":"global"}`)

func activityMaintainQuery(profile appconfig.Profile) json.RawMessage {
	if query := profile.ActivityMaintain.Query; len(query) != 0 {
		return query
	}
	return activityMaintainDefaultQuery
}

// hasResumePlaceholder reports whether the search resolves its resume per
// profile instead of naming one concrete resume.
func hasResumePlaceholder(query json.RawMessage) bool {
	var parsed struct {
		Source string `json:"source"`
		Resume string `json:"resume"`
	}
	if err := json.Unmarshal(query, &parsed); err != nil {
		return false
	}
	return parsed.Source == "similar_resume" &&
		(parsed.Resume == hh.ResumePlaceholderProfile || parsed.Resume == hh.ResumePlaceholderAll)
}

// resumeQueryVariants expands the resume placeholder of a search for the
// profile that will run it. A concrete resume and other sources pass through
// unchanged; $profile yields the profile's own resume and $all yields one query
// per resume declared by the profile.
func resumeQueryVariants(query json.RawMessage, profile appconfig.Profile) ([]json.RawMessage, error) {
	var parsed struct {
		Source string `json:"source"`
		Resume string `json:"resume"`
	}
	if err := json.Unmarshal(query, &parsed); err != nil {
		return nil, err
	}
	if parsed.Source != "similar_resume" ||
		(parsed.Resume != hh.ResumePlaceholderProfile && parsed.Resume != hh.ResumePlaceholderAll) {
		return []json.RawMessage{query}, nil
	}
	// The normalized resume list is the source of truth; alias values cover a
	// profile that was assembled without the config loader, for example in
	// tests, where only the legacy fields are set.
	resumes := make([]string, 0, len(profile.Resumes)+1)
	if primary := strings.TrimSpace(profile.Resume); primary != "" {
		resumes = append(resumes, primary)
	}
	if parsed.Resume == hh.ResumePlaceholderAll {
		for _, id := range profile.ResumeIDs() {
			if id != "" && !slices.Contains(resumes, id) {
				resumes = append(resumes, id)
			}
		}
		aliases := make([]string, 0, len(profile.ResumeAliases))
		for _, value := range profile.ResumeAliases {
			aliases = append(aliases, value)
		}
		sort.Strings(aliases)
		for _, value := range aliases {
			if value != "" && !slices.Contains(resumes, value) {
				resumes = append(resumes, value)
			}
		}
	}
	if len(resumes) == 0 {
		return nil, fmt.Errorf("profile %q has no resume for %s", profile.Tag, parsed.Resume)
	}
	variants := make([]json.RawMessage, 0, len(resumes))
	for _, resume := range resumes {
		document := make(map[string]any, 8)
		if err := json.Unmarshal(query, &document); err != nil {
			return nil, err
		}
		document["resume"] = resume
		encoded, err := json.Marshal(document)
		if err != nil {
			return nil, err
		}
		variants = append(variants, encoded)
	}
	return variants, nil
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

// watchConfigReload applies the hot part of a changed config on SIGHUP: the
// file is fully loaded and validated first, and only then the scheduler is
// resynced. Campaign and search changes still require a restart because they
// register routes in the campaign handler.
type configReloadStatus struct {
	mu     sync.Mutex
	status httpapi.ConfigStatus
}

func (status *configReloadStatus) record(digest string, definitions int) {
	status.mu.Lock()
	defer status.mu.Unlock()
	status.status = httpapi.ConfigStatus{
		Digest: digest, AppliedAt: time.Now().UTC(), Definitions: definitions,
	}
}

func (status *configReloadStatus) recordError(message string) {
	status.mu.Lock()
	defer status.mu.Unlock()
	status.status.LastError = message
}

func (status *configReloadStatus) snapshot() httpapi.ConfigStatus {
	status.mu.Lock()
	defer status.mu.Unlock()
	return status.status
}

func watchConfigReload(
	ctx context.Context,
	configPath string,
	scheduler *jobscheduler.Scheduler,
	bind func(appconfig.Config) error,
	build func(appconfig.Config) ([]jobscheduler.Definition, error),
	refresh func(appconfig.Config) error,
	applyDefinitions func([]jobscheduler.Definition),
	status *configReloadStatus,
) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP)
	defer signal.Stop(signals)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastDigest := ""
	lastError := ""
	apply := func() {
		fresh, err := appconfig.Load(configPath)
		if err != nil {
			if message := err.Error(); message != lastError {
				logf("config reload rejected: %v", err)
				lastError = message
				status.recordError(message)
			}
			return
		}
		lastError = ""
		encoded, err := json.Marshal(fresh)
		if err != nil {
			return
		}
		digest := sha256.Sum256(encoded)
		current := hex.EncodeToString(digest[:])
		if current == lastDigest {
			return
		}
		if err := bind(fresh); err != nil {
			logf("config reload rejected: %v", err)
			status.recordError(err.Error())
			return
		}
		if err := refresh(fresh); err != nil {
			logf("config reload rejected: %v", err)
			status.recordError(err.Error())
			return
		}
		definitions, err := build(fresh)
		if err != nil {
			logf("config reload rejected: %v", err)
			status.recordError(err.Error())
			return
		}
		if err := scheduler.Sync(ctx, definitions); err != nil {
			logf("config reload failed: %v", err)
			status.recordError(err.Error())
			return
		}
		if applyDefinitions != nil {
			applyDefinitions(definitions)
		}
		lastDigest = current
		status.record(current, len(definitions))
		logf("config reloaded: %d scheduled definitions applied", len(definitions))
	}
	apply()
	for {
		select {
		case <-ctx.Done():
			return
		case <-signals:
			apply()
		case <-ticker.C:
			apply()
		}
	}
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
		// No write deadline: browser RPCs may legitimately take up to two
		// minutes and the auth challenge stream is long-lived. A shorter
		// deadline silently cuts the response, and the client sees only an
		// empty reply instead of the real error. Slow clients stay bounded by
		// the read timeouts.
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}
	result := make(chan error, 1)
	workerErrors := make(chan error, len(workers))
	for _, instance := range workers {
		go func(instance *taskworker.Worker) {
			workerErrors <- instance.Run(ctx)
		}(instance)
	}
	go func() {
		logf("job-agent API listening on http://%s", server.Addr)
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
		for profileIndex, target := range job.Action.TargetProfiles() {
			profile := profiles[target]
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
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskResumeTouch, Platform: core.Platform(instance.Name()), ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
		}
	}
	return definitions, nil
}

func sessionRefreshDefinitions(cfg appconfig.Config, refreshers *taskworker.SessionRefresherRegistry) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionProfileSessionRefresh {
			continue
		}
		for profileIndex, target := range job.Action.TargetProfiles() {
			profile := profiles[target]
			profileID := core.ProfileID(profile.Tag)
			if !profile.Enabled || !refreshers.Has(profileID) {
				continue
			}
			payload, err := json.Marshal(core.ProfileSessionRefreshPayload{ProfileID: profileID})
			if err != nil {
				return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
			}
			for index, trigger := range job.Triggers {
				minimum, maximum := trigger.Jitter.Durations()
				definitions = append(definitions, jobscheduler.Definition{
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskProfileSessionRefresh, Platform: core.Platform(hh.Name), ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
		}
	}
	return definitions, nil
}

func browserWorkerHTTPClient() (*browser.HTTPClient, error) {
	baseURL := strings.TrimSpace(os.Getenv("BROWSER_WORKER_URL"))
	token := strings.TrimSpace(os.Getenv("BROWSER_WORKER_TOKEN"))
	if baseURL == "" || token == "" {
		return nil, nil
	}
	return browser.NewHTTPClient(browser.HTTPConfig{BaseURL: baseURL, Token: token})
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
		for profileIndex, target := range job.Action.TargetProfiles() {
			profile := profiles[target]
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
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskResumePublish, Platform: core.Platform(instance.Name()), ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
		}
	}
	return definitions, nil
}

// resumeUpdateDefinitions schedules declared resume state reconciliation with
// an optional publish. A profile whose reader, writer or (when publishing)
// publisher is unavailable is skipped rather than failing startup.
func resumeUpdateDefinitions(
	cfg appconfig.Config,
	resources []core.ProfileStateResource,
	readers map[core.ProfileID]adapter.ProfileStateReader,
	writers *taskworker.ProfileStateWriterRegistry,
	publishers *taskworker.ResumePublisherRegistry,
	platforms map[core.ProfileID]core.Platform,
) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	resourcesByTag := make(map[string]core.ProfileStateResource, len(resources))
	for _, resource := range resources {
		resourcesByTag[resource.Tag] = resource
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionResumeUpdate {
			continue
		}
		resource, exists := resourcesByTag[job.Action.Resource]
		if !exists {
			return nil, fmt.Errorf("job %q references unknown profile state resource %q", job.Tag, job.Action.Resource)
		}
		profileID := resource.ProfileID
		platform, writable := platforms[profileID]
		if readers[profileID] == nil || !writable || !writers.Has(profileID) {
			continue
		}
		if job.Action.Publish && !publishers.Has(profileID) {
			continue
		}
		resumeID := job.Action.Resume
		if resumeID == "" {
			resumeID = profiles[job.Action.Profile].Resume
		}
		payload, err := json.Marshal(core.ResumeUpdatePayload{
			ProfileID: profileID, ResourceTag: resource.Tag, ResumeID: resumeID, Publish: job.Action.Publish,
		})
		if err != nil {
			return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
		}
		for index, trigger := range job.Triggers {
			minimum, maximum := trigger.Jitter.Durations()
			definitions = append(definitions, jobscheduler.Definition{
				JobTag: job.Tag, TriggerIndex: index, Expression: trigger.Expression, Timezone: trigger.Timezone,
				ActionType: core.TaskResumeUpdate, Platform: platform, ProfileID: profileID,
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
		for profileIndex, target := range job.Action.TargetProfiles() {
			profile := profiles[target]
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
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskProfileActivityObserve, Platform: core.Platform(instance.Name()), ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
		}
	}
	return definitions, nil
}

const (
	// systemJobPrefix groups the schedules generated from the per-profile
	// policies. Pausing the prefix addresses every generated job of a profile.
	systemJobPrefix = "system"
	// recentChatPollPages bounds the fast conversation poll to the newest chats;
	// the unread pass inside discovery still covers the whole catalog.
	recentChatPollPages = 3
	// recentChatPollInterval is the internal cadence of that fast poll.
	recentChatPollInterval = 2 * time.Minute
	// sessionRefreshInterval keeps the stored HH browser session warm.
	sessionRefreshInterval = 4 * time.Hour
	// validationRefreshInterval rechecks vacancies that wait for a
	// questionnaire or a test, so closed ones stop waiting for input.
	validationRefreshInterval = 24 * time.Hour
	validationRefreshCount    = 25
	validationRefreshMinAge   = 24 * time.Hour
	// defaultResumeTouchInterval raises the resume to keep it visible.
	defaultResumeTouchInterval = 4 * time.Hour
	// defaultActivityMaintainInterval browses candidate vacancies for activity.
	defaultActivityMaintainInterval = time.Hour
	activityMaintainCount           = 5
	activityMaintainPause           = 20 * time.Second
	// defaultApplicationCleanupInterval rechecks rejected and stale
	// applications; the cleanup job itself stays opt-in.
	defaultApplicationCleanupInterval = 3 * time.Hour
)

// systemJobDescriptions are the built-in names of the generated system jobs.
// configJobDescriptions collects the operator-facing job names: the declared
// descriptions plus the built-in labels of the generated system jobs. It is
// rebuilt on config reload, so renaming a job in the config shows up without a
// restart.
func configJobDescriptions(cfg appconfig.Config) map[string]string {
	descriptions := make(map[string]string, len(cfg.Jobs)+len(systemJobDescriptions))
	for _, job := range cfg.Jobs {
		if description := strings.TrimSpace(job.Description); description != "" {
			descriptions[job.Tag] = description
		}
	}
	for tag, description := range systemJobDescriptions {
		descriptions[tag] = description
	}
	return descriptions
}

var systemJobDescriptions = map[string]string{
	systemJobPrefix + ".state.chats":        "Снятие состояния HH: обход чатов",
	systemJobPrefix + ".state.poll":         "Снятие состояния HH: быстрый опрос непрочитанных",
	systemJobPrefix + ".state.applications": "Снятие состояния HH: состояния откликов",
	systemJobPrefix + ".state.activity":     "Снятие состояния HH: активность и метрики",
	systemJobPrefix + ".session":            "Обновление сессии HH",
	systemJobPrefix + ".validation":         "Перепроверка вакансий с анкетами и тестами",
	systemJobPrefix + ".resume-touch":       "Подъём резюме",
	systemJobPrefix + ".activity-maintain":  "Просмотр вакансий-кандидатов для активности",
	systemJobPrefix + ".cleanup":            "Очистка отказов и устаревших откликов",
}

// systemJobTags lists every generated system job. The dashboard groups them
// separately from the jobs declared in the configuration.
func systemJobTags() []string {
	tags := make([]string, 0, len(systemJobDescriptions))
	for tag := range systemJobDescriptions {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

// profileSystemCapabilities reports which generated jobs a profile can run.
// A nil check means the capability was not evaluated by the caller.
type profileSystemCapabilities struct {
	sessionRefresh     func(core.ProfileID) bool
	validationCheck    func(core.ProfileID) bool
	resumeTouch        func(core.ProfileID) bool
	activityMaintain   func(core.ProfileID) bool
	applicationCleanup func(core.ProfileID) bool
}

func capabilityAllows(check func(core.ProfileID) bool, profileID core.ProfileID) bool {
	return check == nil || check(profileID)
}

// profileSystemDefinitions turns the per-profile policies into the system
// schedules that keep an account fresh and healthy: the state harvest
// (conversations, application states, activity), the HH session refresh, the
// recheck of vacancies with pending questionnaires or tests, the resume touch,
// activity maintenance and the opt-in application cleanup. Operators configure
// only the policies; these jobs are never declared in the config themselves.
func profileSystemDefinitions(cfg appconfig.Config, capabilities profileSystemCapabilities) ([]jobscheduler.Definition, error) {
	definitions := make([]jobscheduler.Definition, 0)
	for profileIndex, profile := range cfg.Profiles {
		if !profile.Enabled {
			continue
		}
		profileID := core.ProfileID(profile.Tag)
		resumeID := strings.TrimSpace(profile.Resume)
		harvestInterval := profile.StateHarvest.JobInterval(time.Hour)
		type systemEntry struct {
			suffix  string
			action  core.TaskType
			payload any
			every   time.Duration
			policy  appconfig.SystemJobPolicy
		}
		entries := make([]systemEntry, 0, 8)
		if profile.StateHarvest.JobEnabled(true) {
			entries = append(entries,
				systemEntry{"state.chats", core.TaskConversationDiscover, core.ConversationDiscoverPayload{ProfileID: profileID}, harvestInterval, profile.StateHarvest},
				systemEntry{"state.poll", core.TaskConversationDiscover, core.ConversationDiscoverPayload{ProfileID: profileID, MaxPages: recentChatPollPages}, recentChatPollInterval, profile.StateHarvest},
				systemEntry{"state.applications", core.TaskApplicationStateSync, core.ApplicationStateSyncPayload{ProfileID: profileID}, harvestInterval, profile.StateHarvest},
			)
			if resumeID != "" {
				entries = append(entries, systemEntry{"state.activity", core.TaskProfileActivityObserve, core.ProfileActivityObservePayload{ProfileID: profileID, ResumeID: resumeID}, harvestInterval, profile.StateHarvest})
			} else {
				logf("system jobs: profile %q has no resume, activity snapshots are skipped", profile.Tag)
			}
		}
		if capabilityAllows(capabilities.sessionRefresh, profileID) {
			entries = append(entries, systemEntry{"session", core.TaskProfileSessionRefresh, core.ProfileSessionRefreshPayload{ProfileID: profileID}, sessionRefreshInterval, appconfig.SystemJobPolicy{}})
		}
		if capabilityAllows(capabilities.validationCheck, profileID) {
			entries = append(entries, systemEntry{"validation", core.TaskApplicationValidationCheck, core.ApplicationValidationRefreshPayload{
				ProfileID: profileID, Count: validationRefreshCount, MinAge: core.Duration(validationRefreshMinAge),
			}, validationRefreshInterval, appconfig.SystemJobPolicy{}})
		}
		if profile.ResumeTouch.JobEnabled(true) && resumeID != "" && capabilityAllows(capabilities.resumeTouch, profileID) {
			entries = append(entries, systemEntry{"resume-touch", core.TaskResumeTouch, core.ResumeTouchPayload{ProfileID: profileID, ResumeID: resumeID}, profile.ResumeTouch.JobInterval(defaultResumeTouchInterval), profile.ResumeTouch})
		}
		if profile.ActivityMaintain.JobEnabled(true) && capabilityAllows(capabilities.activityMaintain, profileID) {
			// The handler opens real vacancies from a search query; without one
			// it has no candidates and the job becomes a no-op.
			entries = append(entries, systemEntry{"activity-maintain", core.TaskProfileActivityMaintain, core.ProfileActivityMaintainPayload{
				ProfileID: profileID, Count: activityMaintainCount, Pause: core.Duration(activityMaintainPause),
				Query: activityMaintainQuery(profile),
			}, profile.ActivityMaintain.JobInterval(defaultActivityMaintainInterval), profile.ActivityMaintain})
		}
		if profile.ApplicationCleanup.JobEnabled(false) && capabilityAllows(capabilities.applicationCleanup, profileID) {
			entries = append(entries, systemEntry{
				"cleanup", core.TaskApplicationRetention, profile.ApplicationCleanup.Retention.Payload(profileID),
				profile.ApplicationCleanup.JobInterval(defaultApplicationCleanupInterval), profile.ApplicationCleanup.SystemJobPolicy,
			})
		}
		for _, entry := range entries {
			payload, err := json.Marshal(entry.payload)
			if err != nil {
				return nil, fmt.Errorf("encode system job payload for profile %q: %w", profile.Tag, err)
			}
			jitterMin, jitterMax := entry.policy.JobJitter(entry.every)
			definitions = append(definitions, jobscheduler.Definition{
				JobTag:       systemJobPrefix + "." + entry.suffix,
				TriggerIndex: triggerIndexForProfile(0, profileIndex, 1),
				Interval:     entry.every, ActionType: entry.action, Platform: core.Platform(hh.Name),
				ProfileID: profileID, Payload: payload,
				JitterMin: jitterMin, JitterMax: jitterMax,
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
		for profileIndex, target := range job.Action.TargetProfiles() {
			profile := profiles[target]
			profileID := core.ProfileID(profile.Tag)
			if !profile.Enabled || !transports.CanDiscover(profileID) {
				continue
			}
			payload, err := json.Marshal(core.ConversationDiscoverPayload{ProfileID: profileID, MaxPages: job.Action.RecentPages})
			if err != nil {
				return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
			}
			instance := instances[profile.Adapter]
			for index, trigger := range job.Triggers {
				minimum, maximum := trigger.Jitter.Durations()
				definitions = append(definitions, jobscheduler.Definition{
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskConversationDiscover, Platform: core.Platform(instance.Name()), ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
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
		for profileIndex, target := range job.Action.TargetProfiles() {
			profile := profiles[target]
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
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskConversationFollowUpSelect, Platform: core.Platform(instance.Name()), ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
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
		for profileIndex, target := range job.Action.TargetProfiles() {
			profile := profiles[target]
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
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskApplicationRetention, Platform: core.Platform(instance.Name()), ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
		}
	}
	return definitions, nil
}

func profileActivityMaintainDefinitions(cfg appconfig.Config, instances map[string]adapter.Adapter, transports *taskworker.ApplicationTransportRegistry) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionProfileActivityMaintain {
			continue
		}
		for profileIndex, target := range job.Action.TargetProfiles() {
			profile := profiles[target]
			profileID := core.ProfileID(profile.Tag)
			if !profile.Enabled {
				continue
			}
			if _, err := transports.ResolveVacancyReader(profileID); err != nil {
				logf("profile activity maintain %q is disabled until profile %q has a browser read session", job.Tag, profile.Tag)
				continue
			}
			var query json.RawMessage
			for _, search := range cfg.Searches {
				var meta struct {
					Source string `json:"source"`
				}
				_ = json.Unmarshal(search.Query, &meta)
				if meta.Source != "global" || !slices.Contains(search.Profiles, target) {
					continue
				}
				query = search.Query
				break
			}
			if len(query) == 0 {
				logf("profile activity maintain %q has no global search for profile %q; using the application queue", job.Tag, profile.Tag)
			}
			payload, err := json.Marshal(core.ProfileActivityMaintainPayload{
				ProfileID: profileID, Count: job.Action.Count, Pause: job.Action.Pause, Query: query,
			})
			if err != nil {
				return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
			}
			instance := instances[profile.Adapter]
			for index, trigger := range job.Triggers {
				minimum, maximum := trigger.Jitter.Durations()
				definitions = append(definitions, jobscheduler.Definition{
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskProfileActivityMaintain, Platform: core.Platform(instance.Name()), ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
		}
	}
	return definitions, nil
}

func applicationValidationRefreshDefinitions(cfg appconfig.Config, instances map[string]adapter.Adapter, transports *taskworker.ApplicationTransportRegistry) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionApplicationValidationCheck {
			continue
		}
		for profileIndex, target := range job.Action.TargetProfiles() {
			profile := profiles[target]
			profileID := core.ProfileID(profile.Tag)
			if !profile.Enabled {
				continue
			}
			if _, err := transports.ResolveVacancyReader(profileID); err != nil {
				logf("application validation refresh %q is disabled until profile %q has a browser read session", job.Tag, profile.Tag)
				continue
			}
			payload, err := json.Marshal(core.ApplicationValidationRefreshPayload{
				ProfileID: profileID, Count: job.Action.Count, MinAge: job.Action.MinAge,
			})
			if err != nil {
				return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
			}
			instance := instances[profile.Adapter]
			for index, trigger := range job.Triggers {
				minimum, maximum := trigger.Jitter.Durations()
				definitions = append(definitions, jobscheduler.Definition{
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskApplicationValidationCheck, Platform: core.Platform(instance.Name()), ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
		}
	}
	return definitions, nil
}

func applicationAnswerCoveredDefinitions(cfg appconfig.Config, instances map[string]adapter.Adapter) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionApplicationAnswerCovered {
			continue
		}
		for profileIndex, target := range job.Action.TargetProfiles() {
			profile := profiles[target]
			profileID := core.ProfileID(profile.Tag)
			if !profile.Enabled {
				continue
			}
			payload, err := json.Marshal(core.ApplicationAnswerCoveredPayload{
				ProfileID: profileID, Limit: job.Action.Count,
			})
			if err != nil {
				return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
			}
			instance := instances[profile.Adapter]
			for index, trigger := range job.Triggers {
				minimum, maximum := trigger.Jitter.Durations()
				definitions = append(definitions, jobscheduler.Definition{
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskApplicationAnswerCovered, Platform: core.Platform(instance.Name()), ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
		}
	}
	return definitions, nil
}

func applicationStateSyncDefinitions(
	cfg appconfig.Config,
	instances map[string]adapter.Adapter,
	observers *taskworker.ApplicationStateObserverRegistry,
) ([]jobscheduler.Definition, error) {
	profiles := make(map[string]appconfig.Profile, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		profiles[profile.Tag] = profile
	}
	definitions := make([]jobscheduler.Definition, 0)
	for _, job := range cfg.Jobs {
		if !job.Enabled || job.Action.Type != appconfig.JobActionApplicationStateSync {
			continue
		}
		for profileIndex, target := range job.Action.TargetProfiles() {
			profile := profiles[target]
			profileID := core.ProfileID(profile.Tag)
			if !profile.Enabled {
				continue
			}
			if !observers.Has(profileID) {
				logf("application state sync %q is disabled until profile %q has a state observer", job.Tag, profile.Tag)
				continue
			}
			payload, err := json.Marshal(core.ApplicationStateSyncPayload{ProfileID: profileID})
			if err != nil {
				return nil, fmt.Errorf("encode job %q action: %w", job.Tag, err)
			}
			instance := instances[profile.Adapter]
			for index, trigger := range job.Triggers {
				minimum, maximum := trigger.Jitter.Durations()
				definitions = append(definitions, jobscheduler.Definition{
					JobTag: job.Tag, TriggerIndex: triggerIndexForProfile(index, profileIndex, len(job.Triggers)),
					Expression: trigger.Expression, Timezone: trigger.Timezone,
					ActionType: core.TaskApplicationStateSync, Platform: core.Platform(instance.Name()), ProfileID: profileID,
					Payload: payload, Priority: job.Priority, JitterMin: minimum, JitterMax: maximum,
				})
			}
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
		BlockedRetryDelay: 5 * time.Minute, MaxAttempts: 10,
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
				logf("reconcile follow-ups: %v", err)
				continue
			}
			if result.TasksCreated > 0 {
				logf("reconciled %d due follow-ups, created %d tasks", result.Due, result.TasksCreated)
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
				logf("reconcile scheduled jobs: %v", err)
			}
		}
	}
}
