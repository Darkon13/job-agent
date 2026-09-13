package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	config.Profiles[0].Applications.Timezone = "Europe/Moscow"
	if err := config.Validate(); err != nil {
		t.Fatalf("HH live defaults: %v", err)
	}
	policy := config.Profiles[0].Applications
	if limit := policy.EffectiveDailyLimit("hh"); limit != 200 {
		t.Fatalf("default HH daily limit = %d", limit)
	}
	minimum, maximum, err := policy.SubmitJitterDurations("hh")
	if err != nil || minimum != 15*time.Second || maximum != 25*time.Second {
		t.Fatalf("default HH submit jitter = %s..%s err=%v", minimum, maximum, err)
	}
	config.Profiles[0].Applications.DailyLimit = 7
	if err := config.Validate(); err != nil {
		t.Fatalf("valid live application policy: %v", err)
	}
	config.Profiles[0].Applications.DailyLimit = 201
	if err := config.Validate(); err == nil {
		t.Fatal("expected HH limit above platform ceiling to fail")
	}
	config.Profiles[0].Applications.DailyLimit = 7
	config.Profiles[0].Applications.SubmitJitter = JitterConfig{Min: "25s", Max: "15s"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected inverted submit jitter to fail")
	}
	config.Profiles[0].Applications.SubmitJitter = JitterConfig{}
	config.Profiles[0].Applications.Timezone = "Mars/Olympus"
	if err := config.Validate(); err == nil {
		t.Fatal("expected invalid application timezone to fail")
	}
}

func TestApplicationTailoringRequiresSubmitResumeAndKnownModelProvider(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true, Resume: "resume-1", Applications: ApplicationPolicy{
			Mode: ApplicationModeSubmit, Timezone: "Europe/Moscow", DailyLimit: 7,
			Tailoring: &ApplicationTailoringPolicy{Skills: &ApplicationTailoringSkillsPolicy{Enabled: true, Maximum: 30}},
		}}},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid tailoring config: %v", err)
	}
	config.Profiles[0].Applications.Tailoring.Skills.Model = &ApplicationModelPolicy{
		Provider: "openai-main", PromptVersion: "v1", Instruction: "Prefer relevant skills", Timeout: "30s",
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected unknown tailoring model provider to fail")
	}
	config.Models = []ModelProviderConfig{{Tag: "openai-main", Type: ModelProviderOpenAIResponses, Model: "gpt-test"}}
	if err := config.Validate(); err != nil {
		t.Fatalf("tailoring model with provider: %v", err)
	}
	config.Profiles[0].Applications.Mode = ApplicationModeDryRun
	if err := config.Validate(); err == nil {
		t.Fatal("expected tailoring in dry_run to fail")
	}
}

func TestApplicationTailoringAboutRequiresMatchingResumeFacts(t *testing.T) {
	facts := ApplicationResumeFacts{
		Tag: "primary-facts", ResumeID: "resume-2", Facts: map[string]any{"position": "Go developer"},
	}
	digest, err := applicationResumeFactsDigest(facts)
	if err != nil {
		t.Fatalf("resume facts digest: %v", err)
	}
	facts.Digest = digest
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Models:   []ModelProviderConfig{{Tag: "openai-main", Type: ModelProviderOpenAIResponses, Model: "gpt-test"}},
		Profiles: []Profile{{
			Tag: "primary", Adapter: "hh-main", Enabled: true, Resume: "resume-1",
			resolvedResumeFacts: &facts,
			Applications: ApplicationPolicy{
				Mode: ApplicationModeSubmit, Timezone: "Europe/Moscow", DailyLimit: 7,
				Tailoring: &ApplicationTailoringPolicy{About: &ApplicationTailoringAboutPolicy{
					Enabled: true, MaximumRunes: 600,
					Model: &ApplicationModelPolicy{Provider: "openai-main", PromptVersion: "v1", Instruction: "Rewrite about", Timeout: "30s"},
				}},
			},
		}},
	}
	config.Profiles[0].resolvedResumeFacts = nil
	if err := config.Validate(); err != nil {
		t.Fatalf("about tailoring without resume facts: %v", err)
	}
	config.Profiles[0].resolvedResumeFacts = &facts
	if err := config.Validate(); err == nil {
		t.Fatal("expected mismatched resume facts to fail")
	}
	config.Profiles[0].resolvedResumeFacts.ResumeID = "resume-1"
	matchDigest, err := applicationResumeFactsDigest(*config.Profiles[0].resolvedResumeFacts)
	if err != nil {
		t.Fatalf("matching resume facts digest: %v", err)
	}
	config.Profiles[0].resolvedResumeFacts.Digest = matchDigest
	if err := config.Validate(); err != nil {
		t.Fatalf("matching resume facts: %v", err)
	}
}

func TestProfileSessionRefreshJobRequiresStateFile(t *testing.T) {
	base := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true, Resume: "resume-1"}},
		Jobs: []Job{{
			Tag: "refresh-primary-session", Enabled: true, Concurrency: JobConcurrencyForbid,
			Triggers: []JobTrigger{{Type: "cron", Expression: "0 */4 * * *", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action:   JobAction{Type: JobActionProfileSessionRefresh, Profile: "primary"},
		}},
	}
	if err := base.Validate(); err == nil {
		t.Fatal("expected session refresh without state_file to fail")
	}
	base.Profiles[0].StateFile = "/tmp/primary.json"
	if err := base.Validate(); err != nil {
		t.Fatalf("valid session refresh job: %v", err)
	}
}

func TestApplicationValidationAction(t *testing.T) {
	base := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{
			Tag: "primary", Adapter: "hh-main", Enabled: true,
			Applications: ApplicationPolicy{Mode: ApplicationModeSubmit, ValidationAction: ApplicationValidationSkip},
		}},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("validation_action skip: %v", err)
	}
	if !base.Profiles[0].Applications.SkipValidation() {
		t.Fatal("skip validation was not detected")
	}
	base.Profiles[0].Applications.ValidationAction = "ignore"
	if err := base.Validate(); err == nil {
		t.Fatal("expected invalid validation_action to fail")
	}
}

func TestProfileContactsValidation(t *testing.T) {
	base := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{
			Tag: "primary", Adapter: "hh-main", Enabled: true,
			Contacts: &ProfileContacts{Email: "user@example.test", Telegram: "@qworteex"},
		}},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid contacts: %v", err)
	}
	empty := base
	empty.Profiles = []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true, Contacts: &ProfileContacts{}}}
	if err := empty.Validate(); err == nil {
		t.Fatal("expected empty contacts to fail")
	}
	badEmail := base
	badEmail.Profiles = []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true, Contacts: &ProfileContacts{Email: "nope"}}}
	if err := badEmail.Validate(); err == nil {
		t.Fatal("expected invalid email to fail")
	}
	lineBreak := base
	lineBreak.Profiles = []Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true, Contacts: &ProfileContacts{Telegram: "a\nb"}}}
	if err := lineBreak.Validate(); err == nil {
		t.Fatal("expected line break in contacts to fail")
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

func TestLoadResolvesApplicationModelsWithFallbackPools(t *testing.T) {
	directory := t.TempDir()
	messageDirectory := filepath.Join(directory, "messages")
	if err := os.Mkdir(messageDirectory, 0o700); err != nil {
		t.Fatalf("create messages directory: %v", err)
	}
	for _, name := range []string{"default.json", "employer.json"} {
		if err := os.WriteFile(filepath.Join(messageDirectory, name), []byte(`{
			"strategy":"first","templates":[{"tag":"safe","template":"Safe fallback"}]
		}`), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	resumeDirectory := filepath.Join(directory, "resumes")
	if err := os.Mkdir(resumeDirectory, 0o700); err != nil {
		t.Fatalf("create resumes directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resumeDirectory, "backend.json"), []byte(`{
		"resume_id":"resume-1",
		"facts":{"headline":"Backend developer","skills":["Go","PostgreSQL"],"commercial_years":3}
	}`), 0o600); err != nil {
		t.Fatalf("write resume facts: %v", err)
	}
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"models":[{"tag":"cover-letter-mini","type":"openai_responses","model":"gpt-test","api_key_env":"TEST_OPENAI_KEY","max_output_tokens":700}],
		"employer_groups":[{"tag":"marketplaces","rules":[{"name":"Ozon Tech"}]}],
		"profiles":[{
			"tag":"primary","adapter":"hh-main","resume":"resume-1","resume_facts_file":"resumes/backend.json","enabled":true,
			"applications":{
				"message_template_file":"messages/default.json",
				"model":{"provider":"cover-letter-mini","prompt_version":"v1","instruction":"Кратко","timeout":"15s"},
				"employer_rules":[{
					"employer_groups":["marketplaces"],"action":"model",
					"message_template_file":"messages/employer.json",
					"model":{"provider":"cover-letter-mini","prompt_version":"marketplace-v1","instruction":"Учитывай профиль компании","timeout":"10s"}
				}]
			}
		}]
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(loaded.Models) != 1 || loaded.Models[0].APIKeyEnvironment() != "TEST_OPENAI_KEY" || loaded.Profiles[0].Applications.Model.Provider != "cover-letter-mini" {
		t.Fatalf("loaded model config = %#v", loaded)
	}
	facts, exists := loaded.Profiles[0].ResolvedResumeFacts()
	if !exists || facts.Tag != "backend" || facts.ResumeID != "resume-1" || !strings.HasPrefix(facts.Digest, "sha256:") || facts.Facts["commercial_years"] != json.Number("3") {
		t.Fatalf("resolved resume facts = %#v, exists=%t", facts, exists)
	}
	facts.Facts["headline"] = "changed"
	againFacts, _ := loaded.Profiles[0].ResolvedResumeFacts()
	if againFacts.Facts["headline"] == "changed" {
		t.Fatal("resolved resume facts leaked mutable state")
	}
	rules := loaded.Profiles[0].Applications.ResolvedEmployerRules()
	if len(rules) != 1 || rules[0].Model == nil || rules[0].Model.PromptVersion != "marketplace-v1" || rules[0].MessagePool == nil || rules[0].MessagePool.Tag != "employer" {
		t.Fatalf("resolved rules = %#v", rules)
	}
	rules[0].Model.Instruction = "changed"
	if loaded.Profiles[0].Applications.ResolvedEmployerRules()[0].Model.Instruction == "changed" {
		t.Fatal("resolved employer model leaked mutable state")
	}
}

func TestConfigValidatesModelProvidersAndFallbacks(t *testing.T) {
	validModel := ModelProviderConfig{Tag: "mini", Type: ModelProviderOpenAIResponses, Model: "gpt-test"}
	resumeFacts := ApplicationResumeFacts{Tag: "backend", ResumeID: "resume-1", Facts: map[string]any{"skills": []any{"Go"}}}
	resumeFacts.Digest, _ = applicationResumeFactsDigest(resumeFacts)
	base := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Models:   []ModelProviderConfig{validModel},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Resume: "resume-1", Enabled: true, resolvedResumeFacts: &resumeFacts, Applications: ApplicationPolicy{
			Message: "fallback",
			Model:   &ApplicationModelPolicy{Provider: "mini", PromptVersion: "v1", Instruction: "Concise", Timeout: "10s"},
		}}},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid model config: %v", err)
	}
	chat := base
	chat.Models = []ModelProviderConfig{{Tag: "mini", Type: ModelProviderOpenAIChat, Model: "deepseek-chat", ReasoningEffort: "none"}}
	if err := chat.Validate(); err != nil {
		t.Fatalf("valid openai_chat model config: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "duplicate provider", mutate: func(config *Config) { config.Models = append(config.Models, validModel) }},
		{name: "unsafe base URL", mutate: func(config *Config) { config.Models[0].BaseURL = "http://models.example.test/v1" }},
		{name: "unknown provider", mutate: func(config *Config) { config.Profiles[0].Applications.Model.Provider = "missing" }},
		{name: "missing fallback", mutate: func(config *Config) { config.Profiles[0].Applications.Message = "" }},
		{name: "missing resume facts", mutate: func(config *Config) { config.Profiles[0].resolvedResumeFacts = nil }},
		{name: "mismatched resume facts", mutate: func(config *Config) { config.Profiles[0].Resume = "resume-2" }},
		{name: "invalid timeout", mutate: func(config *Config) { config.Profiles[0].Applications.Model.Timeout = "0s" }},
		{name: "unsupported provider type", mutate: func(config *Config) { config.Models[0].Type = "unknown" }},
		{name: "invalid reasoning effort", mutate: func(config *Config) { config.Models[0].ReasoningEffort = "max" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Models = append([]ModelProviderConfig(nil), base.Models...)
			candidate.Profiles = append([]Profile(nil), base.Profiles...)
			applications := base.Profiles[0].Applications
			model := *applications.Model
			applications.Model = &model
			candidate.Profiles[0].Applications = applications
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("expected invalid model config to fail")
			}
		})
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

func TestResumePublishJobValidation(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{Tag: "primary", Adapter: "hh-main", Resume: "resume-1", Enabled: true}},
		Jobs: []Job{{
			Tag: "publish-primary", Enabled: true, Concurrency: JobConcurrencyForbid,
			Triggers: []JobTrigger{{Type: "cron", Expression: "30 9 * * *", Timezone: "Europe/Moscow", Misfire: "run_once", Jitter: JitterConfig{Min: "1m", Max: "10m"}}},
			Action:   JobAction{Type: JobActionResumePublish, Profile: "primary"},
		}},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid resume publish job: %v", err)
	}
	config.Profiles[0].Resume = ""
	config.Jobs[0].Action.Resume = ""
	if err := config.Validate(); err == nil {
		t.Fatal("expected publish without resume to fail")
	}
}

func TestResumeUpdateJobValidation(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{
			{Tag: "primary", Adapter: "hh-main", Resume: "resume-1", Enabled: true},
			{Tag: "secondary", Adapter: "hh-main", Resume: "resume-2", Enabled: true},
		},
		Resources: []ProfileStateResourceConfig{{
			Tag: "primary-about", Type: ResourceTypeProfileState, Profile: "primary",
			Ownership: "declared_fields", State: json.RawMessage(`{"resumes":{"resume-1":{"about":"Backend"}}}`),
		}},
		Jobs: []Job{{
			Tag: "refresh-primary-about", Enabled: true, Concurrency: JobConcurrencyForbid,
			Triggers: []JobTrigger{{Type: "cron", Expression: "0 10 * * *", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action:   JobAction{Type: JobActionResumeUpdate, Profile: "primary", Resource: "primary-about"},
		}},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid resume update job: %v", err)
	}
	config.Jobs[0].Action.Resource = "missing"
	if err := config.Validate(); err == nil {
		t.Fatal("expected unknown resource to fail")
	}
	config.Jobs[0].Action.Resource = "primary-about"
	config.Jobs[0].Action.Publish = true
	if err := config.Validate(); err != nil {
		t.Fatalf("publish should fall back to the profile resume: %v", err)
	}
	config.Profiles[0].Resume = ""
	if err := config.Validate(); err == nil {
		t.Fatal("expected publish without resume to fail")
	}
	config.Profiles[0].Resume = "resume-1"
	config.Jobs[0].Action.Profile = "secondary"
	if err := config.Validate(); err == nil {
		t.Fatal("expected a resource of another profile to fail")
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

func TestProfileAnswerModelPolicyValidation(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Models: []ModelProviderConfig{{
			Tag: "mini", Type: ModelProviderOpenAIResponses, Model: "gpt-test", APIKeyEnv: "JOB_AGENT_TEST_MODEL_KEY",
		}},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{{
			Tag: "primary", Adapter: "hh-main", Enabled: true,
			Answers: &AnswerPolicy{Model: &AnswerModelPolicy{
				Provider: "mini", PromptVersion: "v1", Instruction: "Pick one option.", Timeout: "30s",
			}},
		}},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid answer model policy: %v", err)
	}
	config.Profiles[0].Answers.Model.Provider = "missing"
	if err := config.Validate(); err == nil {
		t.Fatal("expected unknown provider to fail")
	}
	config.Profiles[0].Answers.Model = &AnswerModelPolicy{Provider: "mini", PromptVersion: "v1", Instruction: "Pick one.", Timeout: "0s"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected non-positive timeout to fail")
	}
	config.Profiles[0].Answers.Model = &AnswerModelPolicy{Provider: "mini", Instruction: "Pick one.", Timeout: "30s"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected missing prompt version to fail")
	}
}

func TestJobProfileArrayExpandsTargets(t *testing.T) {
	config := Config{
		Database: DatabaseConfig{Driver: "sqlite", Path: "job-agent.db"},
		Adapters: []AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []Profile{
			{Tag: "primary", Adapter: "hh-main", Resume: "resume-1", Enabled: true},
			{Tag: "secondary", Adapter: "hh-main", Resume: "resume-2", Enabled: true},
		},
		Jobs: []Job{{
			Tag: "sync-conversations", Enabled: true, Concurrency: JobConcurrencyForbid,
			Triggers: []JobTrigger{{Type: "cron", Expression: "*/10 * * * *", Timezone: "UTC", Misfire: "run_once"}},
			Action:   JobAction{Type: JobActionConversationSync, Profiles: []string{"primary", "secondary"}},
		}},
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("valid profiles array job: %v", err)
	}
	targets := config.Jobs[0].Action.TargetProfiles()
	if len(targets) != 2 || targets[0] != "primary" || targets[1] != "secondary" {
		t.Fatalf("target profiles = %#v", targets)
	}
	config.Jobs[0].Action.Profile = "primary"
	if err := config.Validate(); err == nil {
		t.Fatal("expected profile and profiles together to fail")
	}
	config.Jobs[0].Action.Profile = ""
	config.Jobs[0].Action.Profiles = []string{"primary", "missing"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected an unknown profile in the array to fail")
	}
}
