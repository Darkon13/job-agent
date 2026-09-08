package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestConfigRequiresSQLiteDatabase(t *testing.T) {
	valid := Config{Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	missing := Config{}
	if err := missing.Validate(); err == nil {
		t.Fatal("expected missing database to fail")
	}
	unsupported := Config{Database: DatabaseConfig{Driver: "postgres", Path: "dsn"}}
	if err := unsupported.Validate(); err == nil {
		t.Fatal("expected unsupported database to fail")
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	config, err := Load("example/config.json")
	if err != nil {
		t.Fatalf("load example config: %v", err)
	}
	if config.Database.Driver != "sqlite" || !json.Valid(config.Searches[0].Query) {
		t.Fatalf("unexpected example config: %#v", config)
	}
}

func TestProfileCredentialsReferenceIsLoadedWithoutReadingSecret(t *testing.T) {
	var profile Profile
	if err := json.Unmarshal([]byte(`{"tag":"primary","adapter":"hh-main","credentials_ref":"file:/run/secrets/hh-primary.json","enabled":true}`), &profile); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	if profile.CredentialsRef != "file:/run/secrets/hh-primary.json" {
		t.Fatalf("credentials ref = %q", profile.CredentialsRef)
	}
}

func TestApplicationPolicyDefaultsToDryRunAndGuardsLiveMode(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true}},
	}
	if err := config.Validate(); err != nil || config.Profiles[0].Applications.ExecutionMode() != ApplicationModeDryRun {
		t.Fatalf("safe default: mode=%q err=%v", config.Profiles[0].Applications.ExecutionMode(), err)
	}
	config.Profiles[0].Applications.Mode = ApplicationModeSubmit
	if err := config.Validate(); err == nil {
		t.Fatal("expected live mode without daily limit to fail")
	}
	config.Profiles[0].Applications.DailyLimit = 7
	config.Profiles[0].Applications.Timezone = "Europe/Moscow"
	if err := config.Validate(); err != nil {
		t.Fatalf("valid live application policy: %v", err)
	}
	config.Profiles[0].Applications.Timezone = "Mars/Olympus"
	if err := config.Validate(); err == nil {
		t.Fatal("expected invalid application timezone to fail")
	}
}

func TestApplicationPolicyRejectsAmbiguousMessageAndDuplicateTerms(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true, Applications: ApplicationPolicy{
			Message: "static", MessageTemplate: "{{.Title}}",
		}}},
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected static and template message conflict")
	}
	config.Profiles[0].Applications.MessageTemplate = ""
	config.Profiles[0].Applications.Qualification.IncludeAny = []string{"Go", " go "}
	if err := config.Validate(); err == nil {
		t.Fatal("expected case-insensitive duplicate term")
	}
}

func TestLoadResolvesExternalApplicationMessageTemplate(t *testing.T) {
	directory := t.TempDir()
	messageDirectory := filepath.Join(directory, "messages")
	if err := os.Mkdir(messageDirectory, 0o700); err != nil {
		t.Fatalf("create messages directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(messageDirectory, "backend.json"), []byte(`{"template":"Здравствуйте, {{.Employer}}!"}`), 0o600); err != nil {
		t.Fatalf("write message template: %v", err)
	}
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[{"tag":"primary","adapter":"hh-main","enabled":true,"applications":{"message_template_file":"messages/backend.json"}}]
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	config, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if actual := config.Profiles[0].Applications.ResolvedMessageTemplate(); actual != "Здравствуйте, {{.Employer}}!" {
		t.Fatalf("resolved template = %q", actual)
	}
	pool, exists := config.Profiles[0].Applications.ResolvedMessagePool()
	if !exists || pool.Tag != "backend" || pool.Strategy != "first" || len(pool.Templates) != 1 || pool.Templates[0].Tag != "default" {
		t.Fatalf("resolved legacy message pool = %#v, exists=%t", pool, exists)
	}
}

func TestLoadResolvesStableMessagePool(t *testing.T) {
	directory := t.TempDir()
	messageDirectory := filepath.Join(directory, "messages")
	if err := os.Mkdir(messageDirectory, 0o700); err != nil {
		t.Fatalf("create messages directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(messageDirectory, "backend.json"), []byte(`{
		"strategy":"stable_hash",
		"templates":[
			{"tag":"concise","template":"Здравствуйте! {{.Vacancy.Title}}"},
			{"tag":"detailed","template":"Здравствуйте, {{.Vacancy.Employer}}!"}
		]
	}`), 0o600); err != nil {
		t.Fatalf("write message pool: %v", err)
	}
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[{"tag":"primary","adapter":"hh-main","enabled":true,"applications":{"message_template_file":"messages/backend.json"}}]
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	pool, exists := loaded.Profiles[0].Applications.ResolvedMessagePool()
	if !exists || pool.Tag != "backend" || pool.Strategy != "stable_hash" || len(pool.Templates) != 2 || pool.Templates[1].Tag != "detailed" {
		t.Fatalf("resolved message pool = %#v, exists=%t", pool, exists)
	}
	pool.Templates[0].Template = "mutated"
	again, _ := loaded.Profiles[0].Applications.ResolvedMessagePool()
	if again.Templates[0].Template == "mutated" {
		t.Fatal("resolved message pool leaked mutable templates")
	}
}

func TestLoadResolvesEmployerRuleMessagePool(t *testing.T) {
	directory := t.TempDir()
	messageDirectory := filepath.Join(directory, "messages")
	if err := os.Mkdir(messageDirectory, 0o700); err != nil {
		t.Fatalf("create messages directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(messageDirectory, "marketplace.json"), []byte(`{
		"strategy":"first",
		"templates":[{"tag":"focused","template":"Здравствуйте, {{.Vacancy.Employer}}!"}]
	}`), 0o600); err != nil {
		t.Fatalf("write employer message pool: %v", err)
	}
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"employer_groups":[{"tag":"marketplaces","rules":[{"name":"Ozon Tech"}]}],
		"profiles":[{
			"tag":"primary","adapter":"hh-main","enabled":true,
			"applications":{"employer_rules":[{
				"employer_groups":["marketplaces"],
				"action":"message_pool",
				"message_template_file":"messages/marketplace.json"
			}]}
		}]
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	rules := loaded.Profiles[0].Applications.ResolvedEmployerRules()
	if len(rules) != 1 || rules[0].Action != "message_pool" || rules[0].MessagePool == nil ||
		rules[0].MessagePool.Tag != "marketplace" || rules[0].MessagePool.Templates[0].Tag != "focused" {
		t.Fatalf("resolved employer rules = %#v", rules)
	}
	rules[0].EmployerGroups[0] = "changed"
	rules[0].MessagePool.Templates[0].Template = "changed"
	again := loaded.Profiles[0].Applications.ResolvedEmployerRules()
	if again[0].EmployerGroups[0] != "marketplaces" || again[0].MessagePool.Templates[0].Template == "changed" {
		t.Fatal("resolved employer rules leaked mutable state")
	}
}

func TestApplicationPolicyValidatesEmployerRules(t *testing.T) {
	base := Config{
		Database:       DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters:       []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		EmployerGroups: []EmployerGroupConfig{{Tag: "known", Rules: []EmployerGroupRuleConfig{{Name: "Example"}}}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true, Applications: ApplicationPolicy{
			EmployerRules: []ApplicationEmployerRule{{EmployerGroups: []string{"known"}, Action: "skip"}},
		}}},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid skip rule: %v", err)
	}

	base.Profiles[0].Applications.EmployerRules[0].EmployerGroups = []string{"missing"}
	if err := base.Validate(); err == nil {
		t.Fatal("expected unknown employer group to fail")
	}
	base.Profiles[0].Applications.EmployerRules[0].EmployerGroups = []string{"known"}
	base.Profiles[0].Applications.EmployerRules[0].Action = "message_pool"
	if err := base.Validate(); err == nil {
		t.Fatal("expected unresolved rule message pool to fail")
	}
	base.Profiles[0].Applications.EmployerRules[0].Action = "skip"
	base.Profiles[0].Applications.EmployerRules[0].MessageTemplateFile = "messages/forbidden.json"
	if err := base.Validate(); err == nil {
		t.Fatal("expected message pool on skip rule to fail")
	}
}

func TestApplicationPolicyRejectsMultipleMessageSources(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true, Applications: ApplicationPolicy{
			Message: "static", MessageTemplateFile: "messages/backend.json", resolvedTemplate: "template",
		}}},
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected multiple application message sources to fail")
	}
}

func TestProfileBootstrapRequiresSourceAndKnownCondition(t *testing.T) {
	base := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
	}
	base.Profiles = []Profile{{
		Tag: "primary", Adapter: "hh-main",
		Bootstrap: &ProfileBootstrap{Source: "/config/profile.json", When: "empty"},
	}}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid bootstrap: %v", err)
	}

	base.Profiles[0].Bootstrap.When = "missing_resume"
	if err := base.Validate(); err == nil {
		t.Fatal("expected unsupported missing-resume bootstrap to be rejected")
	}
	base.Profiles[0].Bootstrap = &ProfileBootstrap{Source: "/config/profile.json", When: "empty", Publish: true}
	if err := base.Validate(); err == nil {
		t.Fatal("expected unsupported bootstrap publication to be rejected")
	}
	base.Profiles[0].Bootstrap = &ProfileBootstrap{When: "empty"}
	if err := base.Validate(); err == nil {
		t.Fatal("expected missing bootstrap source to be rejected")
	}
}

func TestLoadResolvesRelativeProfileBootstrapManifest(t *testing.T) {
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "profile.json")
	if err := os.WriteFile(manifestPath, []byte(`{
		"api_version":"job-agent/v1",
		"kind":"ProfileBootstrap",
		"metadata":{"name":"primary-bootstrap"},
		"spec":{"profile_id":"primary","state":{"profile":{"area":[1]}}}
	}`), 0o600); err != nil {
		t.Fatalf("write bootstrap manifest: %v", err)
	}
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[{"tag":"primary","adapter":"hh-main","enabled":true,"bootstrap":{"source":"profile.json","when":"empty"}}]
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	resource, exists := loaded.Profiles[0].Bootstrap.ResolvedResource()
	if !exists || resource.Tag != "primary-bootstrap" || resource.ProfileID != "primary" || string(resource.State) != `{"profile":{"area":[1]}}` {
		t.Fatalf("resolved bootstrap resource = %#v, exists=%t", resource, exists)
	}
	resource.State[0] = '['
	again, _ := loaded.Profiles[0].Bootstrap.ResolvedResource()
	if string(again.State) != `{"profile":{"area":[1]}}` {
		t.Fatalf("resolved resource was mutated: %s", again.State)
	}
}

func TestLoadRejectsProfileBootstrapForAnotherProfile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "profile.json"), []byte(`{
		"api_version":"job-agent/v1","kind":"ProfileBootstrap","metadata":{"name":"other"},
		"spec":{"profile_id":"secondary","state":{"profile":{"area":[1]}}}
	}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[{"tag":"primary","adapter":"hh-main","bootstrap":{"source":"profile.json","when":"empty"}}]
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(configPath); err == nil {
		t.Fatal("expected cross-profile bootstrap manifest to fail")
	}
}

func TestProfileStateResourcesBuildCanonicalDesiredState(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Resume: "backend", Enabled: true}},
		Resources: []ProfileStateResourceConfig{{
			Tag: "primary-backend", Type: ResourceTypeProfileState, Profile: "primary",
			Ownership: "declared_fields",
			State:     json.RawMessage(`{ "resumes": { "backend": { "about": "Go developer" } } }`),
		}},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid profile state resource: %v", err)
	}
	resources, err := config.BuildProfileStateResources()
	if err != nil {
		t.Fatalf("build profile state resources: %v", err)
	}
	if len(resources) != 1 || resources[0].Tag != "primary-backend" || resources[0].ManifestDigest == "" || string(resources[0].State) != `{"resumes":{"backend":{"about":"Go developer"}}}` {
		t.Fatalf("resources = %#v", resources)
	}
}

func TestProfileStateResourcesRejectUnknownReferencesAndDuplicates(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true}},
		Resources: []ProfileStateResourceConfig{{
			Tag: "state", Type: ResourceTypeProfileState, Profile: "missing", Ownership: "declared_fields",
			State: json.RawMessage(`{"profile":{"first_name":"Иван"}}`),
		}},
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected unknown profile reference to fail")
	}
	config.Resources[0].Profile = "primary"
	config.Resources = append(config.Resources, config.Resources[0])
	if err := config.Validate(); err == nil {
		t.Fatal("expected duplicate resource tag to fail")
	}
}

func TestConfigBuildsEmployerGroupsAndRejectsCycles(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		EmployerGroups: []EmployerGroupConfig{
			{Tag: "ozon", Rules: []EmployerGroupRuleConfig{{Platform: "hh", EmployerID: "2180"}, {Name: "Ozon Tech"}}},
			{Tag: "marketplaces", Include: []string{"ozon"}},
		},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid employer groups: %v", err)
	}
	matcher, err := config.BuildEmployerGroupMatcher()
	if err != nil || !matcher.HasGroup("ozon") || !matcher.HasGroup("marketplaces") {
		t.Fatalf("matcher=%#v err=%v", matcher, err)
	}

	config.EmployerGroups[0].Include = []string{"marketplaces"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected employer group include cycle to fail")
	}
}

func TestServerDefaultsToLoopbackAndRejectsPublicBind(t *testing.T) {
	config := Config{Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"}}
	if err := config.Validate(); err != nil {
		t.Fatalf("validate default server: %v", err)
	}
	if config.Server.ListenAddress() != "127.0.0.1:8080" || config.Server.ReconcileInterval() != 30*time.Second || config.Server.SchedulerInterval() != 30*time.Second {
		t.Fatalf("unexpected server defaults: %#v", config.Server)
	}
	config.Server.Listen = "0.0.0.0:8080"
	if err := config.Validate(); err == nil {
		t.Fatal("expected unauthenticated public bind to fail")
	}
	config.Server.Exposure = ServerExposurePrivate
	if err := config.Validate(); err != nil {
		t.Fatalf("private container bind: %v", err)
	}
	config.Server.Exposure = "public"
	if err := config.Validate(); err == nil {
		t.Fatal("expected unsupported public exposure to fail")
	}
	config.Server = ServerConfig{Listen: "localhost:8080", FollowUpReconcileInterval: "0s"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected non-positive reconcile interval to fail")
	}
}

func TestResumeTouchJobValidation(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Resume: "resume-1", Enabled: true}},
		Jobs: []Job{{
			Tag: "publish-primary", Enabled: true, Concurrency: JobConcurrencyForbid,
			Triggers: []JobTrigger{{Type: "cron", Expression: "15 * * * *", Timezone: "Europe/Moscow", Misfire: "run_once", Jitter: JitterConfig{Min: "1m", Max: "10m"}}},
			Action:   JobAction{Type: JobActionResumeTouch, Profile: "primary"},
		}},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid resume publish job: %v", err)
	}
	config.Jobs[0].Priority = core.TaskPriorityMax + 1
	if err := config.Validate(); err == nil {
		t.Fatal("expected out-of-range job priority to fail")
	}
	config.Jobs[0].Priority = core.TaskPriorityNormal
	config.Jobs[0].Triggers[0].Jitter.Max = "30s"
	if err := config.Validate(); err == nil {
		t.Fatal("expected inverted jitter bounds to fail")
	}
}

func TestConversationFollowUpSelectionJobValidation(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true, Conversations: ConversationPolicy{AllowSend: true}}},
		Jobs: []Job{{
			Tag: "remind-oldest", Enabled: true, Concurrency: JobConcurrencyForbid,
			Triggers: []JobTrigger{{Type: "cron", Expression: "15 11 * * 1-5", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action: JobAction{
				Type: JobActionConversationFollowUpSelect, Profile: "primary",
				FollowUp: &ConversationFollowUpSelectionConfig{
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
	if err := config.Validate(); err != nil {
		t.Fatalf("valid follow-up selection job: %v", err)
	}
	config.Jobs[0].Action.FollowUp.Policy.CancelOnIncoming = false
	if err := config.Validate(); err == nil {
		t.Fatal("expected unsafe follow-up selection policy to fail")
	}
}

func TestConversationSyncJobValidation(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true}},
		Jobs: []Job{{
			Tag: "sync-conversations", Enabled: true, Concurrency: JobConcurrencyForbid,
			Triggers: []JobTrigger{{Type: "cron", Expression: "*/10 * * * *", Timezone: "UTC", Misfire: "run_once"}},
			Action:   JobAction{Type: JobActionConversationSync, Profile: "primary"},
		}},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid conversation sync job: %v", err)
	}
	config.Jobs[0].Action.Profile = "missing"
	if err := config.Validate(); err == nil {
		t.Fatal("expected unknown conversation sync profile to fail")
	}
}

func TestProfileActivityObserveJobValidation(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Resume: "resume-1", Enabled: true}},
		Jobs: []Job{{
			Tag: "observe-primary", Enabled: true, Concurrency: JobConcurrencyForbid,
			Triggers: []JobTrigger{{Type: "cron", Expression: "*/30 * * * *", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action:   JobAction{Type: JobActionProfileActivityObserve, Profile: "primary"},
		}},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid profile activity observation job: %v", err)
	}
	config.Profiles[0].Resume = ""
	if err := config.Validate(); err == nil {
		t.Fatal("expected missing activity observation resume to fail")
	}
}

func TestApplicationCampaignJobValidation(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true}},
		Searches: []Search{{
			Tag: "golang", Adapter: "hh-main", Profiles: []string{"primary"}, Query: json.RawMessage(`{}`),
		}},
		Jobs: []Job{{
			Tag: "daily", Enabled: true, Concurrency: JobConcurrencyForbid,
			Triggers: []JobTrigger{{Type: "cron", Expression: "30 9 * * *", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action: JobAction{
				Type: JobActionApplicationCampaign, Profiles: []string{"primary"}, Routes: []string{"golang"},
				TargetSuccessful: 20, MaxInFlight: 3,
			},
		}},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid application campaign: %v", err)
	}
	config.Jobs[0].Action.Routes = []string{"missing"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected unknown campaign route to fail")
	}
	config.Jobs[0].Action.Routes = []string{"golang"}
	config.Jobs[0].Action.Profiles = []string{"primary", "primary"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected duplicate campaign profile to fail")
	}
}

func TestProfileStateReconcileJobValidation(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true}},
		Resources: []ProfileStateResourceConfig{{
			Tag: "primary-about", Type: ResourceTypeProfileState, Profile: "primary",
			Ownership: core.ProfileStateOwnershipDeclaredFields,
			State:     json.RawMessage(`{"resumes":{"resume-1":{"about":"Backend"}}}`),
		}},
		Jobs: []Job{{
			Tag: "reconcile-about", Enabled: true, Concurrency: JobConcurrencyForbid,
			Triggers: []JobTrigger{{Type: "cron", Expression: "0 9 * * *", Timezone: "UTC", Misfire: "run_once"}},
			Action:   JobAction{Type: JobActionProfileStateReconcile, Resource: "primary-about"},
		}},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid profile state reconcile job: %v", err)
	}
	config.Jobs[0].Action.Resource = "missing"
	if err := config.Validate(); err == nil {
		t.Fatal("expected unknown profile state resource to fail")
	}
}
