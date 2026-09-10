package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	jobscheduler "github.com/Darkon13/job-agent/scheduler"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
	taskworker "github.com/Darkon13/job-agent/worker"
	"github.com/Darkon13/job-agent/workflow"
)

type profileReaderStub struct {
	result adapter.ProfileReadResult
	err    error
	calls  int
}

func (reader *profileReaderStub) ReadProfile(context.Context, core.ProfileID) (adapter.ProfileReadResult, error) {
	reader.calls++
	return reader.result, reader.err
}

type profileAdapterStub struct {
	reader       adapter.ProfileReader
	capabilities []core.Capability
}

type profileStateReaderStub func(context.Context, adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error)

func (reader profileStateReaderStub) ReadProfileState(ctx context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
	return reader(ctx, request)
}

type profileActivityObserverStub struct{}

func (profileActivityObserverStub) ObserveProfileActivity(context.Context, core.ProfileID, string) (adapter.ProfileActivityObservation, error) {
	return adapter.ProfileActivityObservation{ObservedAt: time.Now().UTC()}, nil
}

type conversationTransportStub struct{}

func (conversationTransportStub) SendConversationMessage(context.Context, adapter.ConversationSendCommand) (core.ConversationMessage, error) {
	return core.ConversationMessage{}, errors.New("not implemented")
}

func (conversationTransportStub) MarkConversationRead(context.Context, core.ProfileID, string) error {
	return errors.New("not implemented")
}

func (conversationTransportStub) SyncConversation(context.Context, core.ProfileID, core.ConversationID, string) (adapter.ConversationSyncResult, error) {
	return adapter.ConversationSyncResult{}, errors.New("not implemented")
}

func (conversationTransportStub) DiscoverConversations(context.Context, core.ProfileID) (adapter.ConversationDiscoveryResult, error) {
	return adapter.ConversationDiscoveryResult{}, errors.New("not implemented")
}

func TestParseMainOptions(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      mainOptions
		wantErr   bool
	}{
		{name: "compatible positional config", arguments: []string{"config.json"}, want: mainOptions{configPath: "config.json"}},
		{name: "migrate before startup", arguments: []string{"-migrate-up", "/config/config.json"}, want: mainOptions{configPath: "/config/config.json", migrateUp: true}},
		{name: "missing config", arguments: []string{"-migrate-up"}, wantErr: true},
		{name: "extra positional argument", arguments: []string{"one.json", "two.json"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseMainOptions(test.arguments)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseMainOptions() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("parseMainOptions() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestApplyServerEnvironmentOverridesContainerListener(t *testing.T) {
	cfg := appconfig.Config{
		Database: appconfig.DatabaseConfig{Driver: "sqlite", Path: "/data/job-agent.db"},
		Server:   appconfig.ServerConfig{Listen: "127.0.0.1:8080"},
	}
	values := map[string]string{
		"JOB_AGENT_SERVER_LISTEN":   "0.0.0.0:8080",
		"JOB_AGENT_SERVER_EXPOSURE": appconfig.ServerExposurePrivate,
	}
	if err := applyServerEnvironment(&cfg, func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	}); err != nil {
		t.Fatalf("apply server environment: %v", err)
	}
	if cfg.Server.Listen != "0.0.0.0:8080" || cfg.Server.Exposure != appconfig.ServerExposurePrivate {
		t.Fatalf("unexpected server config: %#v", cfg.Server)
	}
}

func TestApplyServerEnvironmentRejectsUnsafeListener(t *testing.T) {
	cfg := appconfig.Config{
		Database: appconfig.DatabaseConfig{Driver: "sqlite", Path: "/data/job-agent.db"},
		Server:   appconfig.ServerConfig{Listen: "127.0.0.1:8080"},
	}
	if err := applyServerEnvironment(&cfg, func(name string) (string, bool) {
		if name == "JOB_AGENT_SERVER_LISTEN" {
			return "0.0.0.0:8080", true
		}
		return "", false
	}); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected unsafe listener error, got %v", err)
	}
}

func TestJobRunDefinitionsCollapseCompatibleTriggersInWorkflow(t *testing.T) {
	payload := json.RawMessage(`{"profile_id":"primary","resume_id":"resume-1"}`)
	definitions := []jobscheduler.Definition{
		{JobTag: "touch", TriggerIndex: 0, ActionType: core.TaskResumeTouch, Platform: "hh", ProfileID: "primary", Payload: payload, Priority: 50},
		{JobTag: "touch", TriggerIndex: 1, ActionType: core.TaskResumeTouch, Platform: "hh", ProfileID: "primary", Payload: payload, Priority: 50},
	}
	jobWorkflow, err := workflow.NewJobRunWorkflow(brokermemory.NewQueue(), workflow.SystemClock{}, workflow.RandomIDGenerator{}, jobRunDefinitions(definitions))
	if err != nil {
		t.Fatalf("new job run workflow: %v", err)
	}
	listed := jobWorkflow.Definitions()
	if len(listed) != 1 || listed[0].Tag != "touch" || listed[0].Priority != 50 {
		t.Fatalf("unexpected runnable jobs: %#v", listed)
	}
}

func (stub *profileAdapterStub) Name() string                         { return "stub" }
func (stub *profileAdapterStub) Capabilities() []core.Capability      { return stub.capabilities }
func (stub *profileAdapterStub) ValidateSearch(json.RawMessage) error { return nil }
func (stub *profileAdapterStub) Search(context.Context, core.ProfileID, json.RawMessage, string) (core.SearchPage, error) {
	return core.SearchPage{}, errors.New("not implemented")
}
func (stub *profileAdapterStub) ReadVacancy(context.Context, core.ProfileID, core.VacancyKey) (core.Vacancy, error) {
	return core.Vacancy{}, errors.New("not implemented")
}
func (stub *profileAdapterStub) NewProfileReader(core.ProfileID, string) (adapter.ProfileReader, error) {
	return stub.reader, nil
}

func TestProbeProfileAuthorizationsMarksUnauthorizedProfile(t *testing.T) {
	reader := &profileReaderStub{err: &core.OperationError{
		Category: core.ErrorUnauthorized, Operation: "profiles.read", Platform: "stub",
	}}
	profiles := []appconfig.Profile{
		{Tag: "primary", Adapter: "platform", CredentialsRef: "file:/secret", Enabled: true},
		{Tag: "disabled", Adapter: "platform", CredentialsRef: "file:/secret", Enabled: false},
	}
	states, err := probeProfileAuthorizations(context.Background(), profiles, map[string]adapter.Adapter{
		"platform": &profileAdapterStub{reader: reader},
	})
	if err != nil {
		t.Fatalf("probe profiles: %v", err)
	}
	if states["primary"].Status != core.ProfileAuthRequired || states["disabled"].Status != core.ProfileDisabled {
		t.Fatalf("unexpected profile states: %#v", states)
	}
	if reader.calls != 1 {
		t.Fatalf("profile reads = %d, want 1", reader.calls)
	}
}

func TestProbeProfileAuthorizationsRetainsAuthenticatedReader(t *testing.T) {
	reader := &profileReaderStub{result: adapter.ProfileReadResult{ExternalAccountID: "account-42", AuthType: "applicant"}}
	profiles, err := probeProfileAuthorizations(context.Background(), []appconfig.Profile{{
		Tag: "primary", Adapter: "platform", CredentialsRef: "file:/secret", Enabled: true,
	}}, map[string]adapter.Adapter{"platform": &profileAdapterStub{reader: reader}})
	if err != nil {
		t.Fatalf("probe profiles: %v", err)
	}
	runtime := profiles["primary"]
	if runtime.Status != core.ProfileEnabled || runtime.ExternalAccountID != "account-42" || runtime.Reader != reader {
		t.Fatalf("unexpected runtime profile: %#v", runtime)
	}
}

func TestProbeProfileAuthorizationsReturnsTransportFailure(t *testing.T) {
	reader := &profileReaderStub{err: &core.OperationError{
		Category: core.ErrorTemporaryFailure, Operation: "profiles.read", Platform: "stub",
	}}
	_, err := probeProfileAuthorizations(context.Background(), []appconfig.Profile{{
		Tag: "primary", Adapter: "platform", CredentialsRef: "file:/secret", Enabled: true,
	}}, map[string]adapter.Adapter{"platform": &profileAdapterStub{reader: reader}})
	if !core.ErrorIsCategory(err, core.ErrorTemporaryFailure) {
		t.Fatalf("error = %v, want temporary failure", err)
	}
}

func TestApplicationPreparerRejectsInvalidTemplateAtComposition(t *testing.T) {
	_, err := applicationPreparer(appconfig.Profile{Applications: appconfig.ApplicationPolicy{
		MessageTemplate: "{{.Missing}}",
	}}, nil, nil)
	if err == nil {
		t.Fatal("expected invalid application message template")
	}
}

func TestBuildApplicationModelsReadsOnlyConfiguredEnvironment(t *testing.T) {
	configured := []appconfig.ModelProviderConfig{{
		Tag: "mini", Type: appconfig.ModelProviderOpenAIResponses, Model: "gpt-test", APIKeyEnv: "JOB_AGENT_TEST_MODEL_KEY",
	}}
	lookedUp := ""
	models, err := buildApplicationModels(configured, func(name string) (string, bool) {
		lookedUp = name
		return "secret-value", true
	})
	if err != nil || lookedUp != "JOB_AGENT_TEST_MODEL_KEY" || models["mini"] == nil {
		t.Fatalf("models=%#v lookup=%q err=%v", models, lookedUp, err)
	}
	_, err = buildApplicationModels(configured, func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "JOB_AGENT_TEST_MODEL_KEY") || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("missing secret error = %v", err)
	}
}

func TestProfileStateReconcileDefinitionsRequireReadAndWriteCapabilities(t *testing.T) {
	resource, err := core.NewProfileStateResource("primary-about", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"Backend"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	cfg := appconfig.Config{Jobs: []appconfig.Job{{
		Tag: "reconcile-about", Enabled: true,
		Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "0 9 * * *", Timezone: "UTC"}},
		Action:   appconfig.JobAction{Type: appconfig.JobActionProfileStateReconcile, Resource: resource.Tag},
	}}}
	reader := profileStateReaderStub(func(context.Context, adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		return core.NewProfileStateObservation("primary", resource.State, "", time.Now().UTC())
	})
	definitions, err := profileStateReconcileDefinitions(
		cfg, []core.ProfileStateResource{resource},
		map[core.ProfileID]adapter.ProfileStateReader{"primary": reader},
		map[core.ProfileID]core.Platform{"primary": "hh"},
	)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("definitions = %#v err=%v", definitions, err)
	}
	if definitions[0].ActionType != core.TaskProfileStateReconcile || definitions[0].ProfileID != "primary" || definitions[0].Platform != "hh" {
		t.Fatalf("definition = %#v", definitions[0])
	}
	var payload core.ProfileStateReconcilePayload
	if err := json.Unmarshal(definitions[0].Payload, &payload); err != nil || payload.ResourceTag != resource.Tag {
		t.Fatalf("payload = %#v err=%v", payload, err)
	}
	disabled, err := profileStateReconcileDefinitions(cfg, []core.ProfileStateResource{resource}, nil, map[core.ProfileID]core.Platform{"primary": "hh"})
	if err != nil || len(disabled) != 0 {
		t.Fatalf("definition without reader = %#v err=%v", disabled, err)
	}
}

func TestProfileActivityDefinitionsRequireRegisteredObserver(t *testing.T) {
	cfg := appconfig.Config{
		Profiles: []appconfig.Profile{{Tag: "primary", Adapter: "platform", Resume: "resume-1", Enabled: true}},
		Jobs: []appconfig.Job{{
			Tag: "observe-primary", Enabled: true,
			Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "*/30 * * * *", Timezone: "UTC"}},
			Action:   appconfig.JobAction{Type: appconfig.JobActionProfileActivityObserve, Profile: "primary"},
		}},
	}
	registry := taskworker.NewProfileActivityObserverRegistry()
	if err := registry.Register("primary", profileActivityObserverStub{}); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	definitions, err := profileActivityDefinitions(cfg, map[string]adapter.Adapter{"platform": &profileAdapterStub{}}, registry)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("definitions=%#v err=%v", definitions, err)
	}
	if definitions[0].ActionType != core.TaskProfileActivityObserve || definitions[0].ProfileID != "primary" {
		t.Fatalf("definition=%#v", definitions[0])
	}
	var payload core.ProfileActivityObservePayload
	if err := json.Unmarshal(definitions[0].Payload, &payload); err != nil || payload.ProfileID != "primary" || payload.ResumeID != "resume-1" {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
	empty, err := profileActivityDefinitions(cfg, map[string]adapter.Adapter{"platform": &profileAdapterStub{}}, taskworker.NewProfileActivityObserverRegistry())
	if err != nil || len(empty) != 0 {
		t.Fatalf("definitions without observer=%#v err=%v", empty, err)
	}
}

func TestConversationFollowUpSelectionDefinitions(t *testing.T) {
	cfg := appconfig.Config{
		Profiles: []appconfig.Profile{{Tag: "primary", Adapter: "platform", Enabled: true, Conversations: appconfig.ConversationPolicy{AllowSend: true}}},
		Jobs: []appconfig.Job{{
			Tag: "remind-oldest", Enabled: true,
			Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "15 11 * * 1-5", Timezone: "Europe/Moscow"}},
			Action: appconfig.JobAction{
				Type: appconfig.JobActionConversationFollowUpSelect, Profile: "primary",
				FollowUp: &appconfig.ConversationFollowUpSelectionConfig{
					Strategy: core.FollowUpSelectOldestUnanswered, MinimumSilence: core.Duration(72 * time.Hour),
					RunAfter: core.Duration(time.Minute), DeadlineAfter: core.Duration(24 * time.Hour),
					Content: core.MessageContent{Text: "Подскажите, вакансия ещё актуальна?"},
					Policy: core.FollowUpPolicy{
						CancelOnIncoming: true, RequireActiveConversation: true,
						MaxFollowUps: 1, Cooldown: core.Duration(72 * time.Hour),
					},
				},
			},
		}},
	}
	transports := taskworker.NewConversationTransportRegistry()
	if err := transports.Register("primary", conversationTransportStub{}); err != nil {
		t.Fatalf("register conversation transport: %v", err)
	}
	definitions, err := conversationFollowUpSelectionDefinitions(cfg, map[string]adapter.Adapter{
		"platform": &profileAdapterStub{capabilities: []core.Capability{core.CapabilityConversationWrite}},
	}, transports)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("definitions=%#v err=%v", definitions, err)
	}
	if definitions[0].ActionType != core.TaskConversationFollowUpSelect || definitions[0].ProfileID != "primary" {
		t.Fatalf("definition=%#v", definitions[0])
	}
	var payload core.ConversationFollowUpSelectPayload
	if err := json.Unmarshal(definitions[0].Payload, &payload); err != nil || payload.Strategy != core.FollowUpSelectOldestUnanswered || payload.Content.Text == "" {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
	empty, err := conversationFollowUpSelectionDefinitions(cfg, map[string]adapter.Adapter{"platform": &profileAdapterStub{}}, transports)
	if err != nil || len(empty) != 0 {
		t.Fatalf("definitions without conversation write capability=%#v err=%v", empty, err)
	}
}

func TestConversationDiscoveryDefinitionsRequireDiscoverableTransport(t *testing.T) {
	cfg := appconfig.Config{
		Profiles: []appconfig.Profile{{Tag: "primary", Adapter: "platform", Enabled: true}},
		Jobs: []appconfig.Job{{
			Tag: "sync-conversations", Enabled: true,
			Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "*/10 * * * *", Timezone: "UTC"}},
			Action:   appconfig.JobAction{Type: appconfig.JobActionConversationSync, Profile: "primary"},
		}},
	}
	transports := taskworker.NewConversationTransportRegistry()
	if err := transports.Register("primary", conversationTransportStub{}); err != nil {
		t.Fatalf("register conversation transport: %v", err)
	}
	definitions, err := conversationDiscoveryDefinitions(cfg, map[string]adapter.Adapter{
		"platform": &profileAdapterStub{},
	}, transports)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("definitions=%#v err=%v", definitions, err)
	}
	if definitions[0].ActionType != core.TaskConversationDiscover || definitions[0].ProfileID != "primary" {
		t.Fatalf("definition=%#v", definitions[0])
	}
	var payload core.ConversationDiscoverPayload
	if err := json.Unmarshal(definitions[0].Payload, &payload); err != nil || payload.ProfileID != "primary" {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
	empty, err := conversationDiscoveryDefinitions(cfg, map[string]adapter.Adapter{"platform": &profileAdapterStub{}}, taskworker.NewConversationTransportRegistry())
	if err != nil || len(empty) != 0 {
		t.Fatalf("definitions without transport=%#v err=%v", empty, err)
	}
}

func TestConfigureApplicationCampaignsBuildsScheduledRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reader := &profileReaderStub{}
	instance := &profileAdapterStub{reader: reader}
	cfg := appconfig.Config{
		Searches: []appconfig.Search{{
			Tag: "golang", Adapter: "platform", Profiles: []string{"primary"}, Query: json.RawMessage(`{}`),
		}},
		Jobs: []appconfig.Job{{
			Tag: "daily", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
			Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "30 9 * * *", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action: appconfig.JobAction{
				Type: appconfig.JobActionApplicationCampaign, Profiles: []string{"primary"}, Routes: []string{"golang"},
				TargetSuccessful: 20, MaxInFlight: 3,
			},
		}},
	}
	handler, definitions, routes, jobs, err := configureApplicationCampaigns(
		cfg, map[string]adapter.Adapter{"platform": instance}, map[core.ProfileID]profileRuntime{
			"primary": {Status: core.ProfileEnabled, Reader: reader},
		}, store,
	)
	if err != nil {
		t.Fatalf("configure campaigns: %v", err)
	}
	if handler == nil || jobs != 1 || len(definitions) != 1 {
		t.Fatalf("handler=%v jobs=%d definitions=%d", handler != nil, jobs, len(definitions))
	}
	if definitions[0].ActionType != core.TaskApplicationCampaign || definitions[0].ProfileID != "primary" {
		t.Fatalf("definition=%#v", definitions[0])
	}
	if _, exists := routes["golang"]; !exists {
		t.Fatalf("campaign-owned routes=%#v", routes)
	}
	var payload core.ApplicationCampaignPayload
	if err := json.Unmarshal(definitions[0].Payload, &payload); err != nil || payload.Validate() != nil {
		t.Fatalf("campaign payload=%#v decode_err=%v validation_err=%v", payload, err, payload.Validate())
	}
}

func TestConfigureApplicationCampaignsRequiresEveryProfileAPIReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reader := &profileReaderStub{}
	cfg := appconfig.Config{
		Searches: []appconfig.Search{{
			Tag: "golang", Adapter: "platform", Profiles: []string{"primary", "secondary"}, Query: json.RawMessage(`{}`),
		}},
		Jobs: []appconfig.Job{{
			Tag: "daily", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
			Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "30 9 * * *", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action: appconfig.JobAction{
				Type: appconfig.JobActionApplicationCampaign, Profiles: []string{"primary", "secondary"}, Routes: []string{"golang"},
				TargetSuccessful: 20, MaxInFlight: 3,
			},
		}},
	}
	_, definitions, _, jobs, err := configureApplicationCampaigns(
		cfg, map[string]adapter.Adapter{"platform": &profileAdapterStub{reader: reader}}, map[core.ProfileID]profileRuntime{
			"primary":   {Status: core.ProfileEnabled, Reader: reader},
			"secondary": {Status: core.ProfileEnabled},
		}, store,
	)
	if err != nil {
		t.Fatalf("configure campaigns: %v", err)
	}
	if jobs != 0 || len(definitions) != 0 {
		t.Fatalf("jobs=%d definitions=%d, want disabled campaign", jobs, len(definitions))
	}
}

func TestConfigureApplicationCampaignsAcceptsReadOnlyBrowserProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reader := &profileAdapterStub{}
	cfg := appconfig.Config{
		Searches: []appconfig.Search{{
			Tag: "golang", Adapter: "platform", Profiles: []string{"primary"}, Query: json.RawMessage(`{}`),
		}},
		Jobs: []appconfig.Job{{
			Tag: "dry-run", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
			Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "30 9 * * *", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action: appconfig.JobAction{
				Type: appconfig.JobActionApplicationCampaign, Profiles: []string{"primary"}, Routes: []string{"golang"},
				TargetSuccessful: 1, MaxInFlight: 1,
			},
		}},
	}
	_, definitions, _, jobs, err := configureApplicationCampaigns(
		cfg, map[string]adapter.Adapter{"platform": reader}, map[core.ProfileID]profileRuntime{
			"primary": {Status: core.ProfileEnabled, BrowserReader: reader},
		}, store,
	)
	if err != nil {
		t.Fatalf("configure browser campaign: %v", err)
	}
	if jobs != 1 || len(definitions) != 1 {
		t.Fatalf("jobs=%d definitions=%d", jobs, len(definitions))
	}
}
