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

	base.Profiles[0].Bootstrap.When = "always"
	if err := base.Validate(); err == nil {
		t.Fatal("expected unconditional bootstrap to be rejected")
	}
	base.Profiles[0].Bootstrap = &ProfileBootstrap{When: "empty"}
	if err := base.Validate(); err == nil {
		t.Fatal("expected missing bootstrap source to be rejected")
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
	config.Jobs[0].Triggers[0].Jitter.Max = "30s"
	if err := config.Validate(); err == nil {
		t.Fatal("expected inverted jitter bounds to fail")
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
