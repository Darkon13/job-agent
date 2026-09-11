package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
	applicationoperator "github.com/Darkon13/job-agent/operator"
	"github.com/robfig/cron/v3"
)

type Config struct {
	Database       DatabaseConfig               `json:"database"`
	Adapters       []AdapterConfig              `json:"adapters"`
	Profiles       []Profile                    `json:"profiles"`
	Searches       []Search                     `json:"searches"`
	Resources      []ProfileStateResourceConfig `json:"resources,omitempty"`
	EmployerGroups []EmployerGroupConfig        `json:"employer_groups,omitempty"`
	Models         []ModelProviderConfig        `json:"models,omitempty"`
	Jobs           []Job                        `json:"jobs,omitempty"`
	Server         ServerConfig                 `json:"server,omitempty"`
}

type EmployerGroupConfig struct {
	Tag     string                    `json:"tag"`
	Rules   []EmployerGroupRuleConfig `json:"rules,omitempty"`
	Include []string                  `json:"include,omitempty"`
}

type EmployerGroupRuleConfig struct {
	Platform   core.Platform `json:"platform,omitempty"`
	EmployerID string        `json:"employer_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

const ModelProviderOpenAIResponses = "openai_responses"

type ModelProviderConfig struct {
	Tag             string `json:"tag"`
	Type            string `json:"type"`
	Model           string `json:"model"`
	BaseURL         string `json:"base_url,omitempty"`
	APIKeyEnv       string `json:"api_key_env,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
}

func (config ModelProviderConfig) APIKeyEnvironment() string {
	if strings.TrimSpace(config.APIKeyEnv) == "" {
		return "OPENAI_API_KEY"
	}
	return strings.TrimSpace(config.APIKeyEnv)
}

func (c Config) BuildEmployerGroupMatcher() (*applicationoperator.EmployerGroupMatcher, error) {
	groups := make([]applicationoperator.EmployerGroupConfig, 0, len(c.EmployerGroups))
	for _, configured := range c.EmployerGroups {
		group := applicationoperator.EmployerGroupConfig{Tag: configured.Tag, Include: append([]string(nil), configured.Include...)}
		for _, rule := range configured.Rules {
			group.Rules = append(group.Rules, applicationoperator.EmployerGroupRuleConfig{
				Platform: rule.Platform, EmployerID: rule.EmployerID, Name: rule.Name,
			})
		}
		groups = append(groups, group)
	}
	return applicationoperator.NewEmployerGroupMatcher(groups)
}

const ResourceTypeProfileState = "profile_state"

type ProfileStateResourceConfig struct {
	Tag       string          `json:"tag"`
	Type      string          `json:"type"`
	Profile   string          `json:"profile"`
	Ownership string          `json:"ownership"`
	State     json.RawMessage `json:"state"`
}

const (
	JobActionResumeTouch                = "resume.touch"
	JobActionApplicationCampaign        = "application.campaign"
	JobActionProfileStateReconcile      = "profile_state.reconcile"
	JobActionProfileActivityObserve     = "profile.activity.observe"
	JobActionConversationSync           = "conversation.sync"
	JobActionConversationFollowUpSelect = "conversation.follow_up.select"
	JobActionApplicationRetention       = "application.retention"
	JobConcurrencyForbid                = "forbid"
	ApplicationModeDryRun               = "dry_run"
	ApplicationModeApproval             = "approval"
	ApplicationModeSubmit               = "submit"
	DefaultHHDailyApplicationLimit      = 200
)

const (
	defaultHHApplicationJitterMin = 15 * time.Second
	defaultHHApplicationJitterMax = 25 * time.Second
)

type Job struct {
	Tag         string            `json:"tag"`
	Enabled     bool              `json:"enabled"`
	Priority    core.TaskPriority `json:"priority,omitempty"`
	Triggers    []JobTrigger      `json:"triggers"`
	Concurrency string            `json:"concurrency"`
	Action      JobAction         `json:"action"`
}

type JobTrigger struct {
	Type       string       `json:"type"`
	Expression string       `json:"expression"`
	Timezone   string       `json:"timezone"`
	Misfire    string       `json:"misfire"`
	Jitter     JitterConfig `json:"jitter,omitempty"`
}

type JitterConfig struct {
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`
}

func (jitter JitterConfig) Durations() (time.Duration, time.Duration) {
	minimum, _ := time.ParseDuration(jitter.Min)
	maximum, _ := time.ParseDuration(jitter.Max)
	return minimum, maximum
}

type JobAction struct {
	Type             string                               `json:"type"`
	Resource         string                               `json:"resource,omitempty"`
	Profile          string                               `json:"profile,omitempty"`
	Resume           string                               `json:"resume,omitempty"`
	Profiles         []string                             `json:"profiles,omitempty"`
	Routes           []string                             `json:"routes,omitempty"`
	TargetSuccessful int                                  `json:"target_successful,omitempty"`
	MaxInFlight      int                                  `json:"max_in_flight,omitempty"`
	FollowUp         *ConversationFollowUpSelectionConfig `json:"follow_up,omitempty"`
	Retention        *ApplicationRetentionConfig          `json:"retention,omitempty"`
}

type ApplicationRetentionConfig struct {
	StaleAfter     core.Duration `json:"stale_after"`
	RemoveRejected *bool         `json:"remove_rejected,omitempty"`
}

func (configured ApplicationRetentionConfig) Payload(profileID core.ProfileID) core.ApplicationRetentionPayload {
	removeRejected := true
	if configured.RemoveRejected != nil {
		removeRejected = *configured.RemoveRejected
	}
	return core.ApplicationRetentionPayload{
		ProfileID: profileID, StaleAfter: configured.StaleAfter, RemoveRejected: removeRejected,
	}
}

type ConversationFollowUpSelectionConfig struct {
	Strategy       core.FollowUpSelectionStrategy `json:"strategy"`
	MinimumSilence core.Duration                  `json:"minimum_silence"`
	RunAfter       core.Duration                  `json:"run_after"`
	DeadlineAfter  core.Duration                  `json:"deadline_after,omitempty"`
	Content        core.MessageContent            `json:"content"`
	Policy         core.FollowUpPolicy            `json:"policy"`
}

func (configured ConversationFollowUpSelectionConfig) Payload(profileID core.ProfileID) core.ConversationFollowUpSelectPayload {
	return core.ConversationFollowUpSelectPayload{
		ProfileID: profileID, Strategy: configured.Strategy, MinimumSilence: configured.MinimumSilence,
		RunAfter: configured.RunAfter, DeadlineAfter: configured.DeadlineAfter,
		Content: configured.Content, Policy: configured.Policy,
	}
}

type ServerConfig struct {
	Listen                     string `json:"listen,omitempty"`
	Exposure                   string `json:"exposure,omitempty"`
	FollowUpReconcileInterval  string `json:"follow_up_reconcile_interval,omitempty"`
	SchedulerReconcileInterval string `json:"scheduler_reconcile_interval,omitempty"`
}

const (
	ServerExposureLoopback = "loopback"
	ServerExposurePrivate  = "private"
)

func (config ServerConfig) ExposureMode() string {
	if config.Exposure == "" {
		return ServerExposureLoopback
	}
	return config.Exposure
}

func (config ServerConfig) SchedulerInterval() time.Duration {
	if config.SchedulerReconcileInterval == "" {
		return 30 * time.Second
	}
	value, _ := time.ParseDuration(config.SchedulerReconcileInterval)
	return value
}

func (config ServerConfig) ListenAddress() string {
	if config.Listen == "" {
		return "127.0.0.1:8080"
	}
	return config.Listen
}

func (config ServerConfig) ReconcileInterval() time.Duration {
	if config.FollowUpReconcileInterval == "" {
		return 30 * time.Second
	}
	value, _ := time.ParseDuration(config.FollowUpReconcileInterval)
	return value
}

type DatabaseConfig struct {
	Driver string `json:"driver"`
	Path   string `json:"path"`
}

type AdapterConfig struct {
	Tag      string          `json:"tag"`
	Type     string          `json:"type"`
	Settings json.RawMessage `json:"settings,omitempty"`
}

type Profile struct {
	Tag                 string             `json:"tag"`
	Adapter             string             `json:"adapter"`
	Resume              string             `json:"resume,omitempty"`
	ResumeFactsFile     string             `json:"resume_facts_file,omitempty"`
	CredentialsRef      string             `json:"credentials_ref,omitempty"`
	StateFile           string             `json:"state_file,omitempty"`
	Enabled             bool               `json:"enabled"`
	Bootstrap           *ProfileBootstrap  `json:"bootstrap,omitempty"`
	Applications        ApplicationPolicy  `json:"applications,omitempty"`
	Conversations       ConversationPolicy `json:"conversations,omitempty"`
	resolvedResumeFacts *ApplicationResumeFacts
}

type ApplicationResumeFacts struct {
	Tag      string         `json:"tag"`
	ResumeID string         `json:"resume_id"`
	Digest   string         `json:"digest"`
	Facts    map[string]any `json:"facts"`
}

func (profile Profile) ResolvedResumeFacts() (ApplicationResumeFacts, bool) {
	if profile.resolvedResumeFacts == nil {
		return ApplicationResumeFacts{}, false
	}
	return cloneApplicationResumeFacts(*profile.resolvedResumeFacts), true
}

type ConversationPolicy struct {
	AllowSend     bool `json:"allow_send,omitempty"`
	AllowMarkRead bool `json:"allow_mark_read,omitempty"`
}

type ApplicationPolicy struct {
	Mode                  string                      `json:"mode,omitempty"`
	Message               string                      `json:"message,omitempty"`
	MessageTemplate       string                      `json:"message_template,omitempty"`
	MessageTemplateFile   string                      `json:"message_template_file,omitempty"`
	Model                 *ApplicationModelPolicy     `json:"model,omitempty"`
	EmployerRules         []ApplicationEmployerRule   `json:"employer_rules,omitempty"`
	Qualification         ApplicationQualification    `json:"qualification,omitempty"`
	DailyLimit            int                         `json:"daily_limit,omitempty"`
	SubmitJitter          JitterConfig                `json:"submit_jitter,omitempty"`
	Timezone              string                      `json:"timezone,omitempty"`
	AllowVisibilityChange bool                        `json:"allow_visibility_change,omitempty"`
	Tailoring             *ApplicationTailoringPolicy `json:"tailoring,omitempty"`
	resolvedTemplate      string
	resolvedMessagePool   *ApplicationMessagePool
}

type ApplicationEmployerRule struct {
	EmployerGroups      []string                `json:"employer_groups"`
	Action              string                  `json:"action"`
	MessageTemplateFile string                  `json:"message_template_file,omitempty"`
	Model               *ApplicationModelPolicy `json:"model,omitempty"`
	resolvedMessagePool *ApplicationMessagePool
}

type ApplicationModelPolicy struct {
	Provider      string `json:"provider"`
	PromptVersion string `json:"prompt_version"`
	Instruction   string `json:"instruction"`
	Timeout       string `json:"timeout"`
}

type ResolvedApplicationEmployerRule struct {
	EmployerGroups []string
	Action         string
	MessagePool    *ApplicationMessagePool
	Model          *ApplicationModelPolicy
}

type ApplicationMessageTemplate struct {
	Tag      string `json:"tag"`
	Template string `json:"template"`
}

type ApplicationMessagePool struct {
	Tag       string                       `json:"tag"`
	Strategy  string                       `json:"strategy"`
	Templates []ApplicationMessageTemplate `json:"templates"`
}

type ApplicationQualification struct {
	IncludeAny []string `json:"include_any,omitempty"`
	ExcludeAny []string `json:"exclude_any,omitempty"`
}

type ApplicationTailoringPolicy struct {
	Skills *ApplicationTailoringSkillsPolicy `json:"skills,omitempty"`
}

type ApplicationTailoringSkillsPolicy struct {
	Enabled bool                    `json:"enabled,omitempty"`
	Maximum int                     `json:"maximum,omitempty"`
	Model   *ApplicationModelPolicy `json:"model,omitempty"`
}

func (policy ApplicationPolicy) TailoringSkills() (ApplicationTailoringSkillsPolicy, bool) {
	if policy.Tailoring == nil || policy.Tailoring.Skills == nil || !policy.Tailoring.Skills.Enabled {
		return ApplicationTailoringSkillsPolicy{}, false
	}
	return *policy.Tailoring.Skills, true
}

func (policy ApplicationPolicy) ExecutionMode() string {
	if policy.Mode == "" {
		return ApplicationModeDryRun
	}
	return policy.Mode
}

func (policy ApplicationPolicy) LocationName() string {
	if policy.Timezone == "" {
		return "UTC"
	}
	return policy.Timezone
}

func (policy ApplicationPolicy) EffectiveDailyLimit(adapterType string) int {
	if policy.DailyLimit != 0 {
		return policy.DailyLimit
	}
	if adapterType == "hh" {
		return DefaultHHDailyApplicationLimit
	}
	return 0
}

func (policy ApplicationPolicy) SubmitJitterDurations(adapterType string) (time.Duration, time.Duration, error) {
	minimumText := strings.TrimSpace(policy.SubmitJitter.Min)
	maximumText := strings.TrimSpace(policy.SubmitJitter.Max)
	if minimumText == "" && maximumText == "" {
		if adapterType == "hh" {
			return defaultHHApplicationJitterMin, defaultHHApplicationJitterMax, nil
		}
		return 0, 0, nil
	}
	if minimumText == "" || maximumText == "" {
		return 0, 0, errors.New("submit_jitter requires both min and max")
	}
	minimum, err := time.ParseDuration(minimumText)
	if err != nil {
		return 0, 0, fmt.Errorf("submit_jitter min: %w", err)
	}
	maximum, err := time.ParseDuration(maximumText)
	if err != nil {
		return 0, 0, fmt.Errorf("submit_jitter max: %w", err)
	}
	if minimum <= 0 || maximum < minimum {
		return 0, 0, errors.New("submit_jitter requires 0 < min <= max")
	}
	return minimum, maximum, nil
}

func (policy ApplicationPolicy) ResolvedMessageTemplate() string {
	if policy.resolvedTemplate != "" {
		return policy.resolvedTemplate
	}
	return policy.MessageTemplate
}

func (policy ApplicationPolicy) ResolvedMessagePool() (ApplicationMessagePool, bool) {
	if policy.resolvedMessagePool == nil {
		return ApplicationMessagePool{}, false
	}
	return cloneApplicationMessagePool(*policy.resolvedMessagePool), true
}

func (policy ApplicationPolicy) ResolvedEmployerRules() []ResolvedApplicationEmployerRule {
	result := make([]ResolvedApplicationEmployerRule, 0, len(policy.EmployerRules))
	for _, configured := range policy.EmployerRules {
		resolved := ResolvedApplicationEmployerRule{
			EmployerGroups: append([]string(nil), configured.EmployerGroups...),
			Action:         configured.Action,
		}
		if configured.Model != nil {
			model := *configured.Model
			resolved.Model = &model
		}
		if configured.resolvedMessagePool != nil {
			pool := cloneApplicationMessagePool(*configured.resolvedMessagePool)
			resolved.MessagePool = &pool
		}
		result = append(result, resolved)
	}
	return result
}

func cloneApplicationMessagePool(pool ApplicationMessagePool) ApplicationMessagePool {
	result := pool
	result.Templates = append([]ApplicationMessageTemplate(nil), pool.Templates...)
	return result
}

// ProfileBootstrap schedules one idempotent initial profile fill after auth.
// Source is resolved by the config builder and should normally be a mounted
// read-only JSON file.
type ProfileBootstrap struct {
	Source           string `json:"source"`
	When             string `json:"when"`
	Publish          bool   `json:"publish,omitempty"`
	resolvedResource *core.ProfileStateResource
}

func (bootstrap ProfileBootstrap) ResolvedResource() (core.ProfileStateResource, bool) {
	if bootstrap.resolvedResource == nil {
		return core.ProfileStateResource{}, false
	}
	resource := *bootstrap.resolvedResource
	resource.State = append(json.RawMessage(nil), resource.State...)
	return resource, true
}

type Search struct {
	Tag                string            `json:"tag"`
	Adapter            string            `json:"adapter"`
	Profiles           []string          `json:"profiles"`
	Priority           core.TaskPriority `json:"priority"`
	TargetApplications int               `json:"target_applications"`
	Fallback           string            `json:"fallback,omitempty"`
	Query              json.RawMessage   `json:"query"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.resolveApplicationMessageFiles(filepath.Dir(path)); err != nil {
		return Config{}, err
	}
	if err := cfg.resolveApplicationResumeFactsFiles(filepath.Dir(path)); err != nil {
		return Config{}, err
	}
	if err := cfg.resolveProfileBootstrapFiles(filepath.Dir(path)); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

const maximumApplicationResumeFactsBytes = 128 << 10

func (c *Config) resolveApplicationResumeFactsFiles(baseDirectory string) error {
	for index := range c.Profiles {
		profile := &c.Profiles[index]
		reference := strings.TrimSpace(profile.ResumeFactsFile)
		if reference == "" {
			continue
		}
		path := reference
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDirectory, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("profile %q resume_facts_file: %w", profile.Tag, err)
		}
		if len(data) > maximumApplicationResumeFactsBytes {
			return fmt.Errorf("profile %q resume_facts_file exceeds %d bytes", profile.Tag, maximumApplicationResumeFactsBytes)
		}
		var file struct {
			ResumeID string         `json:"resume_id"`
			Facts    map[string]any `json:"facts"`
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&file); err != nil {
			return fmt.Errorf("profile %q resume_facts_file: decode: %w", profile.Tag, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return fmt.Errorf("profile %q resume_facts_file contains trailing JSON", profile.Tag)
		}
		facts := ApplicationResumeFacts{
			Tag:      strings.TrimSuffix(filepath.Base(reference), filepath.Ext(reference)),
			ResumeID: strings.TrimSpace(file.ResumeID), Facts: file.Facts,
		}
		if err := validateApplicationResumeFacts(facts); err != nil {
			return fmt.Errorf("profile %q resume_facts_file: %w", profile.Tag, err)
		}
		facts.Digest, err = applicationResumeFactsDigest(facts)
		if err != nil {
			return fmt.Errorf("profile %q resume_facts_file: %w", profile.Tag, err)
		}
		profile.resolvedResumeFacts = &facts
	}
	return nil
}

func validateApplicationResumeFacts(facts ApplicationResumeFacts) error {
	if strings.TrimSpace(facts.Tag) == "" || strings.TrimSpace(facts.ResumeID) == "" || len(facts.Facts) == 0 {
		return errors.New("resume facts require file tag, resume_id and a non-empty facts object")
	}
	entries := 0
	if err := validateApplicationResumeFactValue(facts.Facts, 0, &entries); err != nil {
		return err
	}
	return nil
}

func validateApplicationResumeFactValue(value any, depth int, entries *int) error {
	if depth > 8 {
		return errors.New("resume facts exceed maximum nesting depth")
	}
	*entries = *entries + 1
	if *entries > 512 {
		return errors.New("resume facts exceed maximum entry count")
	}
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if strings.TrimSpace(key) == "" || len(key) > 128 {
				return errors.New("resume facts contain an empty or oversized key")
			}
			if err := validateApplicationResumeFactValue(child, depth+1, entries); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range item {
			if err := validateApplicationResumeFactValue(child, depth+1, entries); err != nil {
				return err
			}
		}
	case string:
		if strings.TrimSpace(item) == "" || len(item) > 10_000 {
			return errors.New("resume facts contain an empty or oversized string")
		}
	case json.Number, bool:
	case nil:
		return errors.New("resume facts must not contain null values")
	default:
		return fmt.Errorf("resume facts contain unsupported value type %T", value)
	}
	return nil
}

func applicationResumeFactsDigest(facts ApplicationResumeFacts) (string, error) {
	canonical, err := json.Marshal(struct {
		ResumeID string         `json:"resume_id"`
		Facts    map[string]any `json:"facts"`
	}{ResumeID: facts.ResumeID, Facts: facts.Facts})
	if err != nil {
		return "", fmt.Errorf("encode resume facts: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func cloneApplicationResumeFacts(facts ApplicationResumeFacts) ApplicationResumeFacts {
	result := facts
	result.Facts = cloneApplicationResumeFactValue(facts.Facts).(map[string]any)
	return result
}

func cloneApplicationResumeFactValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(item))
		for key, child := range item {
			result[key] = cloneApplicationResumeFactValue(child)
		}
		return result
	case []any:
		result := make([]any, len(item))
		for index, child := range item {
			result[index] = cloneApplicationResumeFactValue(child)
		}
		return result
	default:
		return item
	}
}

func (c *Config) resolveProfileBootstrapFiles(baseDirectory string) error {
	for index := range c.Profiles {
		bootstrap := c.Profiles[index].Bootstrap
		if bootstrap == nil || strings.TrimSpace(bootstrap.Source) == "" {
			continue
		}
		path := bootstrap.Source
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDirectory, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("profile %q bootstrap source: %w", c.Profiles[index].Tag, err)
		}
		manifest, err := core.DecodeProfileBootstrapManifest(data)
		if err != nil {
			return fmt.Errorf("profile %q bootstrap source: %w", c.Profiles[index].Tag, err)
		}
		if manifest.Spec.ProfileID != core.ProfileID(c.Profiles[index].Tag) {
			return fmt.Errorf("profile %q bootstrap source belongs to profile %q", c.Profiles[index].Tag, manifest.Spec.ProfileID)
		}
		resource, err := manifest.Resource()
		if err != nil {
			return fmt.Errorf("profile %q bootstrap source: %w", c.Profiles[index].Tag, err)
		}
		bootstrap.resolvedResource = &resource
	}
	return nil
}

func (c *Config) resolveApplicationMessageFiles(baseDirectory string) error {
	for index := range c.Profiles {
		policy := &c.Profiles[index].Applications
		if strings.TrimSpace(policy.MessageTemplateFile) != "" {
			pool, template, err := resolveApplicationMessageFile(
				baseDirectory,
				policy.MessageTemplateFile,
				fmt.Sprintf("profile %q message_template_file", c.Profiles[index].Tag),
			)
			if err != nil {
				return err
			}
			policy.resolvedTemplate = template
			policy.resolvedMessagePool = &pool
		}
		for ruleIndex := range policy.EmployerRules {
			rule := &policy.EmployerRules[ruleIndex]
			if strings.TrimSpace(rule.MessageTemplateFile) == "" {
				continue
			}
			pool, _, err := resolveApplicationMessageFile(
				baseDirectory,
				rule.MessageTemplateFile,
				fmt.Sprintf("profile %q employer rule %d message_template_file", c.Profiles[index].Tag, ruleIndex),
			)
			if err != nil {
				return err
			}
			rule.resolvedMessagePool = &pool
		}
	}
	return nil
}

func resolveApplicationMessageFile(baseDirectory, reference, label string) (ApplicationMessagePool, string, error) {
	path := reference
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDirectory, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ApplicationMessagePool{}, "", fmt.Errorf("%s: %w", label, err)
	}
	var file struct {
		Template  string                       `json:"template,omitempty"`
		Strategy  string                       `json:"strategy,omitempty"`
		Templates []ApplicationMessageTemplate `json:"templates,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return ApplicationMessagePool{}, "", fmt.Errorf("%s: decode: %w", label, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ApplicationMessagePool{}, "", fmt.Errorf("%s contains trailing JSON", label)
	}
	if strings.TrimSpace(file.Template) != "" && len(file.Templates) != 0 {
		return ApplicationMessagePool{}, "", fmt.Errorf("%s cannot mix template and templates", label)
	}
	legacyTemplate := ""
	if strings.TrimSpace(file.Template) != "" {
		legacyTemplate = file.Template
		file.Strategy = applicationoperator.MessagePoolFirst
		file.Templates = []ApplicationMessageTemplate{{Tag: "default", Template: file.Template}}
	}
	if len(file.Templates) == 0 {
		return ApplicationMessagePool{}, "", fmt.Errorf("%s requires template or templates", label)
	}
	if strings.TrimSpace(file.Strategy) == "" {
		file.Strategy = applicationoperator.MessagePoolFirst
	}
	poolTag := strings.TrimSuffix(filepath.Base(reference), filepath.Ext(reference))
	return ApplicationMessagePool{Tag: poolTag, Strategy: file.Strategy, Templates: file.Templates}, legacyTemplate, nil
}

func validateApplicationMessagePool(pool ApplicationMessagePool) error {
	if strings.TrimSpace(pool.Tag) == "" || len(pool.Templates) == 0 {
		return errors.New("message pool requires tag and templates")
	}
	if pool.Strategy != applicationoperator.MessagePoolFirst && pool.Strategy != applicationoperator.MessagePoolStableHash {
		return fmt.Errorf("message pool has unsupported strategy %q", pool.Strategy)
	}
	seenTemplates := make(map[string]struct{}, len(pool.Templates))
	for _, candidate := range pool.Templates {
		tag := strings.TrimSpace(candidate.Tag)
		if tag == "" || strings.TrimSpace(candidate.Template) == "" {
			return errors.New("message pool contains an empty tag or template")
		}
		if _, exists := seenTemplates[tag]; exists {
			return fmt.Errorf("message pool contains duplicate template tag %q", tag)
		}
		seenTemplates[tag] = struct{}{}
	}
	return nil
}

func validateModelProvider(config ModelProviderConfig) error {
	if strings.TrimSpace(config.Tag) == "" || strings.TrimSpace(config.Model) == "" {
		return errors.New("model provider requires tag and model")
	}
	if config.Type != ModelProviderOpenAIResponses {
		return fmt.Errorf("model provider %q has unsupported type %q", config.Tag, config.Type)
	}
	if environment := config.APIKeyEnvironment(); strings.Contains(environment, "=") || strings.ContainsAny(environment, " \t\r\n") {
		return fmt.Errorf("model provider %q has invalid api_key_env", config.Tag)
	}
	if config.MaxOutputTokens < 0 || config.MaxOutputTokens > 32768 {
		return fmt.Errorf("model provider %q max_output_tokens must be between 1 and 32768 when set", config.Tag)
	}
	if strings.TrimSpace(config.BaseURL) != "" {
		parsed, err := url.Parse(config.BaseURL)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("model provider %q base_url must be an absolute URL without credentials, query or fragment", config.Tag)
		}
		if parsed.Scheme != "https" {
			ip := net.ParseIP(parsed.Hostname())
			if parsed.Scheme != "http" || (!strings.EqualFold(parsed.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback())) {
				return fmt.Errorf("model provider %q base_url must use HTTPS or loopback HTTP", config.Tag)
			}
		}
	}
	return nil
}

func validateApplicationModelPolicy(label string, policy *ApplicationModelPolicy, providers map[string]struct{}) error {
	if policy == nil {
		return nil
	}
	if _, exists := providers[strings.TrimSpace(policy.Provider)]; !exists {
		return fmt.Errorf("%s references unknown model provider %q", label, policy.Provider)
	}
	if strings.TrimSpace(policy.PromptVersion) == "" || strings.TrimSpace(policy.Instruction) == "" {
		return fmt.Errorf("%s model requires prompt_version and instruction", label)
	}
	timeout, err := time.ParseDuration(policy.Timeout)
	if err != nil || timeout <= 0 || timeout > 5*time.Minute {
		return fmt.Errorf("%s model timeout must be a positive duration no greater than 5m", label)
	}
	return nil
}

func (c Config) Validate() error {
	if c.Database.Driver != "sqlite" {
		return fmt.Errorf("database driver must be %q", "sqlite")
	}
	if c.Database.Path == "" {
		return fmt.Errorf("sqlite database requires path")
	}
	host, _, err := net.SplitHostPort(c.Server.ListenAddress())
	if err != nil {
		return fmt.Errorf("server listen address: %w", err)
	}
	switch c.Server.ExposureMode() {
	case ServerExposureLoopback:
		if host == "localhost" {
			break
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("server listen must use a loopback address when exposure is %q", ServerExposureLoopback)
		}
	case ServerExposurePrivate:
		// Private exposure is intended only for an un-published container network.
		// The separately deployed dashboard is the single ingress endpoint.
	default:
		return fmt.Errorf("server exposure must be %q or %q", ServerExposureLoopback, ServerExposurePrivate)
	}
	if c.Server.FollowUpReconcileInterval != "" {
		interval, err := time.ParseDuration(c.Server.FollowUpReconcileInterval)
		if err != nil || interval <= 0 {
			return fmt.Errorf("server follow_up_reconcile_interval must be a positive duration")
		}
	}
	if c.Server.SchedulerReconcileInterval != "" {
		interval, err := time.ParseDuration(c.Server.SchedulerReconcileInterval)
		if err != nil || interval <= 0 {
			return fmt.Errorf("server scheduler_reconcile_interval must be a positive duration")
		}
	}
	adapters := make(map[string]string, len(c.Adapters))
	for _, item := range c.Adapters {
		if item.Tag == "" || item.Type == "" {
			return fmt.Errorf("every adapter requires tag and type")
		}
		if _, exists := adapters[item.Tag]; exists {
			return fmt.Errorf("duplicate adapter tag %q", item.Tag)
		}
		adapters[item.Tag] = item.Type
	}
	modelProviders := make(map[string]struct{}, len(c.Models))
	for _, model := range c.Models {
		if err := validateModelProvider(model); err != nil {
			return err
		}
		tag := strings.TrimSpace(model.Tag)
		if _, exists := modelProviders[tag]; exists {
			return fmt.Errorf("duplicate model provider tag %q", tag)
		}
		modelProviders[tag] = struct{}{}
	}
	employerMatcher, err := c.BuildEmployerGroupMatcher()
	if err != nil {
		return err
	}

	profiles := make(map[string]struct{}, len(c.Profiles))
	profileConfigs := make(map[string]Profile, len(c.Profiles))
	for _, profile := range c.Profiles {
		if profile.Tag == "" || profile.Adapter == "" {
			return fmt.Errorf("every profile requires tag and adapter")
		}
		adapterType, exists := adapters[profile.Adapter]
		if !exists {
			return fmt.Errorf("profile %q references unknown adapter %q", profile.Tag, profile.Adapter)
		}
		if _, exists := profiles[profile.Tag]; exists {
			return fmt.Errorf("duplicate profile tag %q", profile.Tag)
		}
		resumeFacts, hasResumeFacts := profile.ResolvedResumeFacts()
		if strings.TrimSpace(profile.ResumeFactsFile) != "" && !hasResumeFacts {
			return fmt.Errorf("profile %q resume_facts_file must be resolved by config loader", profile.Tag)
		}
		if hasResumeFacts {
			if err := validateApplicationResumeFacts(resumeFacts); err != nil {
				return fmt.Errorf("profile %q: %w", profile.Tag, err)
			}
			digest, err := applicationResumeFactsDigest(resumeFacts)
			if err != nil || resumeFacts.Digest != digest {
				return fmt.Errorf("profile %q resume facts digest does not match its contents", profile.Tag)
			}
			if strings.TrimSpace(profile.Resume) == "" || resumeFacts.ResumeID != strings.TrimSpace(profile.Resume) {
				return fmt.Errorf("profile %q resume facts belong to resume %q, configured resume is %q", profile.Tag, resumeFacts.ResumeID, profile.Resume)
			}
		}
		if profile.Bootstrap != nil {
			if strings.TrimSpace(profile.Bootstrap.Source) == "" {
				return fmt.Errorf("profile %q bootstrap requires source", profile.Tag)
			}
			if profile.Bootstrap.When != "empty" {
				return fmt.Errorf("profile %q bootstrap currently supports only when=empty; missing_resume requires resume creation", profile.Tag)
			}
			if profile.Bootstrap.Publish {
				return fmt.Errorf("profile %q bootstrap publish requires resume publication support", profile.Tag)
			}
		}
		switch profile.Applications.ExecutionMode() {
		case ApplicationModeDryRun, ApplicationModeApproval:
		case ApplicationModeSubmit:
			limit := profile.Applications.EffectiveDailyLimit(adapterType)
			if limit < 1 {
				return fmt.Errorf("profile %q live applications require a positive daily_limit", profile.Tag)
			}
			if adapterType == "hh" && limit > DefaultHHDailyApplicationLimit {
				return fmt.Errorf("profile %q HH daily_limit must not exceed %d", profile.Tag, DefaultHHDailyApplicationLimit)
			}
		default:
			return fmt.Errorf("profile %q has unknown application mode %q", profile.Tag, profile.Applications.Mode)
		}
		if profile.Applications.DailyLimit < 0 {
			return fmt.Errorf("profile %q application daily_limit must not be negative", profile.Tag)
		}
		jitterMin, jitterMax, err := profile.Applications.SubmitJitterDurations(adapterType)
		if err != nil {
			return fmt.Errorf("profile %q applications: %w", profile.Tag, err)
		}
		if profile.Applications.ExecutionMode() == ApplicationModeSubmit && (jitterMin <= 0 || jitterMax < jitterMin) {
			return fmt.Errorf("profile %q live applications require positive submit_jitter", profile.Tag)
		}
		if profile.Applications.Timezone != "" {
			if _, err := time.LoadLocation(profile.Applications.Timezone); err != nil {
				return fmt.Errorf("profile %q application timezone: %w", profile.Tag, err)
			}
		}
		if skills, enabled := profile.Applications.TailoringSkills(); enabled {
			if profile.Applications.ExecutionMode() != ApplicationModeSubmit {
				return fmt.Errorf("profile %q application tailoring requires submit mode", profile.Tag)
			}
			if strings.TrimSpace(profile.Resume) == "" {
				return fmt.Errorf("profile %q application tailoring requires a resume", profile.Tag)
			}
			if skills.Maximum < 1 {
				return fmt.Errorf("profile %q application tailoring skills require a positive maximum", profile.Tag)
			}
			if skills.Model != nil {
				if err := validateApplicationModelPolicy(fmt.Sprintf("profile %q application tailoring", profile.Tag), skills.Model, modelProviders); err != nil {
					return err
				}
			}
		}
		messageSources := 0
		for _, value := range []string{profile.Applications.Message, profile.Applications.MessageTemplate, profile.Applications.MessageTemplateFile} {
			if strings.TrimSpace(value) != "" {
				messageSources++
			}
		}
		if messageSources > 1 {
			return fmt.Errorf("profile %q applications must choose message, message_template or message_template_file", profile.Tag)
		}
		if profile.Applications.Model != nil {
			if messageSources != 1 {
				return fmt.Errorf("profile %q application model requires exactly one message fallback", profile.Tag)
			}
			if !hasResumeFacts {
				return fmt.Errorf("profile %q application model requires resume_facts_file", profile.Tag)
			}
			if err := validateApplicationModelPolicy(fmt.Sprintf("profile %q applications", profile.Tag), profile.Applications.Model, modelProviders); err != nil {
				return err
			}
		}
		if profile.Applications.MessageTemplateFile != "" && profile.Applications.resolvedMessagePool == nil {
			return fmt.Errorf("profile %q message_template_file must be resolved by config loader", profile.Tag)
		}
		if pool := profile.Applications.resolvedMessagePool; pool != nil {
			if err := validateApplicationMessagePool(*pool); err != nil {
				return fmt.Errorf("profile %q: %w", profile.Tag, err)
			}
		}
		for ruleIndex, rule := range profile.Applications.EmployerRules {
			if len(rule.EmployerGroups) == 0 {
				return fmt.Errorf("profile %q employer rule %d requires at least one employer group", profile.Tag, ruleIndex)
			}
			seenGroups := make(map[string]struct{}, len(rule.EmployerGroups))
			for _, value := range rule.EmployerGroups {
				tag := strings.TrimSpace(value)
				if !employerMatcher.HasGroup(tag) {
					return fmt.Errorf("profile %q employer rule %d references unknown employer group %q", profile.Tag, ruleIndex, tag)
				}
				if _, exists := seenGroups[tag]; exists {
					return fmt.Errorf("profile %q employer rule %d contains duplicate employer group %q", profile.Tag, ruleIndex, tag)
				}
				seenGroups[tag] = struct{}{}
			}
			ruleLabel := fmt.Sprintf("profile %q employer rule %d", profile.Tag, ruleIndex)
			switch rule.Action {
			case applicationoperator.EmployerRuleSkip, applicationoperator.EmployerRuleReview:
				if strings.TrimSpace(rule.MessageTemplateFile) != "" || rule.resolvedMessagePool != nil || rule.Model != nil {
					return fmt.Errorf("%s action %q cannot use message_template_file or model", ruleLabel, rule.Action)
				}
			case applicationoperator.EmployerRuleMessagePool:
				if strings.TrimSpace(rule.MessageTemplateFile) == "" || rule.resolvedMessagePool == nil || rule.Model != nil {
					return fmt.Errorf("%s action %q requires a resolved message_template_file and no model", ruleLabel, rule.Action)
				}
				if err := validateApplicationMessagePool(*rule.resolvedMessagePool); err != nil {
					return fmt.Errorf("%s: %w", ruleLabel, err)
				}
			case applicationoperator.EmployerRuleModel:
				if strings.TrimSpace(rule.MessageTemplateFile) == "" || rule.resolvedMessagePool == nil || rule.Model == nil {
					return fmt.Errorf("%s action %q requires model and resolved message_template_file fallback", ruleLabel, rule.Action)
				}
				if !hasResumeFacts {
					return fmt.Errorf("%s action %q requires profile resume_facts_file", ruleLabel, rule.Action)
				}
				if err := validateApplicationMessagePool(*rule.resolvedMessagePool); err != nil {
					return fmt.Errorf("%s: %w", ruleLabel, err)
				}
				if err := validateApplicationModelPolicy(ruleLabel, rule.Model, modelProviders); err != nil {
					return err
				}
			default:
				return fmt.Errorf("profile %q employer rule %d has unsupported action %q", profile.Tag, ruleIndex, rule.Action)
			}
		}
		for field, terms := range map[string][]string{
			"include_any": profile.Applications.Qualification.IncludeAny,
			"exclude_any": profile.Applications.Qualification.ExcludeAny,
		} {
			seen := make(map[string]struct{}, len(terms))
			for _, value := range terms {
				term := strings.ToLower(strings.TrimSpace(value))
				if term == "" {
					return fmt.Errorf("profile %q applications %s contains an empty term", profile.Tag, field)
				}
				if _, exists := seen[term]; exists {
					return fmt.Errorf("profile %q applications %s contains duplicate term %q", profile.Tag, field, value)
				}
				seen[term] = struct{}{}
			}
		}
		profiles[profile.Tag] = struct{}{}
		profileConfigs[profile.Tag] = profile
	}
	profileStateResources, err := c.BuildProfileStateResources()
	if err != nil {
		return err
	}
	resources := make(map[string]core.ProfileID, len(profileStateResources))
	for _, resource := range profileStateResources {
		resources[resource.Tag] = resource.ProfileID
	}
	searches := make(map[string]Search, len(c.Searches))
	for _, search := range c.Searches {
		if search.Tag == "" || search.Adapter == "" {
			return fmt.Errorf("every search requires tag and adapter")
		}
		if _, exists := adapters[search.Adapter]; !exists {
			return fmt.Errorf("search %q references unknown adapter %q", search.Tag, search.Adapter)
		}
		if _, exists := searches[search.Tag]; exists {
			return fmt.Errorf("duplicate search tag %q", search.Tag)
		}
		if len(search.Profiles) == 0 {
			return fmt.Errorf("search %q requires at least one profile", search.Tag)
		}
		if err := search.Priority.Validate(); err != nil {
			return fmt.Errorf("search %q: %w", search.Tag, err)
		}
		for _, profile := range search.Profiles {
			if _, exists := profiles[profile]; !exists {
				return fmt.Errorf("search %q references unknown profile %q", search.Tag, profile)
			}
		}
		searches[search.Tag] = search
	}
	for _, search := range c.Searches {
		if search.Fallback != "" {
			if _, exists := searches[search.Fallback]; !exists {
				return fmt.Errorf("search %q references unknown fallback %q", search.Tag, search.Fallback)
			}
		}
	}
	jobs := make(map[string]struct{}, len(c.Jobs))
	for _, job := range c.Jobs {
		if job.Tag == "" {
			return fmt.Errorf("every job requires tag")
		}
		if _, exists := jobs[job.Tag]; exists {
			return fmt.Errorf("duplicate job tag %q", job.Tag)
		}
		jobs[job.Tag] = struct{}{}
		if err := job.Priority.Validate(); err != nil {
			return fmt.Errorf("job %q: %w", job.Tag, err)
		}
		if !job.Enabled {
			continue
		}
		if job.Concurrency != JobConcurrencyForbid {
			return fmt.Errorf("job %q concurrency must be %q", job.Tag, JobConcurrencyForbid)
		}
		if len(job.Triggers) == 0 {
			return fmt.Errorf("job %q requires at least one trigger", job.Tag)
		}
		for index, trigger := range job.Triggers {
			if err := validateJobTrigger(trigger); err != nil {
				return fmt.Errorf("job %q trigger %d: %w", job.Tag, index, err)
			}
		}
		switch job.Action.Type {
		case JobActionResumeTouch, JobActionProfileActivityObserve:
			if _, exists := profiles[job.Action.Profile]; !exists {
				return fmt.Errorf("job %q references unknown profile %q", job.Tag, job.Action.Profile)
			}
			resume := job.Action.Resume
			if resume == "" {
				for _, profile := range c.Profiles {
					if profile.Tag == job.Action.Profile {
						resume = profile.Resume
						break
					}
				}
			}
			if resume == "" {
				return fmt.Errorf("job %q %s requires resume", job.Tag, job.Action.Type)
			}
		case JobActionConversationSync:
			if _, exists := profiles[job.Action.Profile]; !exists {
				return fmt.Errorf("job %q references unknown profile %q", job.Tag, job.Action.Profile)
			}
		case JobActionConversationFollowUpSelect:
			if _, exists := profiles[job.Action.Profile]; !exists {
				return fmt.Errorf("job %q references unknown profile %q", job.Tag, job.Action.Profile)
			}
			if !profileConfigs[job.Action.Profile].Conversations.AllowSend {
				return fmt.Errorf("job %q requires conversations.allow_send for profile %q", job.Tag, job.Action.Profile)
			}
			if job.Action.FollowUp == nil {
				return fmt.Errorf("job %q requires follow_up selection settings", job.Tag)
			}
			if err := job.Action.FollowUp.Payload(core.ProfileID(job.Action.Profile)).Validate(); err != nil {
				return fmt.Errorf("job %q follow_up: %w", job.Tag, err)
			}
		case JobActionApplicationRetention:
			if _, exists := profiles[job.Action.Profile]; !exists {
				return fmt.Errorf("job %q references unknown profile %q", job.Tag, job.Action.Profile)
			}
			if job.Action.Retention == nil {
				return fmt.Errorf("job %q requires application retention settings", job.Tag)
			}
			if err := job.Action.Retention.Payload(core.ProfileID(job.Action.Profile)).Validate(); err != nil {
				return fmt.Errorf("job %q retention: %w", job.Tag, err)
			}
		case JobActionApplicationCampaign:
			if err := validateApplicationCampaignAction(job, profiles, searches); err != nil {
				return err
			}
		case JobActionProfileStateReconcile:
			if _, exists := resources[job.Action.Resource]; !exists {
				return fmt.Errorf("job %q references unknown profile state resource %q", job.Tag, job.Action.Resource)
			}
		default:
			return fmt.Errorf("job %q has unsupported action %q", job.Tag, job.Action.Type)
		}
	}
	return nil
}

func (c Config) BuildProfileStateResources() ([]core.ProfileStateResource, error) {
	profiles := make(map[string]struct{}, len(c.Profiles))
	for _, profile := range c.Profiles {
		profiles[profile.Tag] = struct{}{}
	}
	tags := make(map[string]struct{}, len(c.Resources))
	resources := make([]core.ProfileStateResource, 0, len(c.Resources))
	for _, configured := range c.Resources {
		if strings.TrimSpace(configured.Tag) == "" {
			return nil, errors.New("every resource requires tag")
		}
		if _, exists := tags[configured.Tag]; exists {
			return nil, fmt.Errorf("duplicate resource tag %q", configured.Tag)
		}
		tags[configured.Tag] = struct{}{}
		if configured.Type != ResourceTypeProfileState {
			return nil, fmt.Errorf("resource %q has unsupported type %q", configured.Tag, configured.Type)
		}
		if _, exists := profiles[configured.Profile]; !exists {
			return nil, fmt.Errorf("resource %q references unknown profile %q", configured.Tag, configured.Profile)
		}
		resource, err := core.NewProfileStateResource(
			configured.Tag, core.ProfileID(configured.Profile), configured.Ownership, configured.State,
		)
		if err != nil {
			return nil, fmt.Errorf("resource %q: %w", configured.Tag, err)
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func validateApplicationCampaignAction(job Job, profiles map[string]struct{}, searches map[string]Search) error {
	action := job.Action
	if action.TargetSuccessful < 1 || action.MaxInFlight < 1 {
		return fmt.Errorf("job %q application campaign requires positive target_successful and max_in_flight", job.Tag)
	}
	if len(action.Profiles) == 0 || len(action.Routes) == 0 {
		return fmt.Errorf("job %q application campaign requires profiles and routes", job.Tag)
	}
	seenProfiles := make(map[string]struct{}, len(action.Profiles))
	for _, profile := range action.Profiles {
		if _, exists := profiles[profile]; !exists {
			return fmt.Errorf("job %q references unknown profile %q", job.Tag, profile)
		}
		if _, exists := seenProfiles[profile]; exists {
			return fmt.Errorf("job %q contains duplicate campaign profile %q", job.Tag, profile)
		}
		seenProfiles[profile] = struct{}{}
	}
	seenRoutes := make(map[string]struct{}, len(action.Routes))
	adapterTag := ""
	for _, route := range action.Routes {
		search, exists := searches[route]
		if !exists {
			return fmt.Errorf("job %q references unknown campaign route %q", job.Tag, route)
		}
		if _, exists := seenRoutes[route]; exists {
			return fmt.Errorf("job %q contains duplicate campaign route %q", job.Tag, route)
		}
		seenRoutes[route] = struct{}{}
		if adapterTag == "" {
			adapterTag = search.Adapter
		} else if search.Adapter != adapterTag {
			return fmt.Errorf("job %q campaign routes must use one adapter", job.Tag)
		}
		routeProfiles := make(map[string]struct{}, len(search.Profiles))
		for _, profile := range search.Profiles {
			routeProfiles[profile] = struct{}{}
		}
		for _, profile := range action.Profiles {
			if _, exists := routeProfiles[profile]; !exists {
				return fmt.Errorf("job %q campaign route %q does not target profile %q", job.Tag, route, profile)
			}
		}
	}
	return nil
}

func validateJobTrigger(trigger JobTrigger) error {
	if trigger.Type != "cron" {
		return fmt.Errorf("unsupported trigger type %q", trigger.Type)
	}
	location, err := time.LoadLocation(trigger.Timezone)
	if err != nil {
		return fmt.Errorf("invalid timezone %q: %w", trigger.Timezone, err)
	}
	if _, err := cron.ParseStandard("CRON_TZ=" + location.String() + " " + trigger.Expression); err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	if trigger.Misfire != "run_once" {
		return fmt.Errorf("misfire must be %q", "run_once")
	}
	minimum, maximum := trigger.Jitter.Durations()
	if (trigger.Jitter.Min != "" && minimum < 0) || (trigger.Jitter.Max != "" && maximum < 0) {
		return errors.New("jitter durations must not be negative")
	}
	if trigger.Jitter.Min != "" {
		if _, err := time.ParseDuration(trigger.Jitter.Min); err != nil {
			return fmt.Errorf("invalid jitter min: %w", err)
		}
	}
	if trigger.Jitter.Max != "" {
		if _, err := time.ParseDuration(trigger.Jitter.Max); err != nil {
			return fmt.Errorf("invalid jitter max: %w", err)
		}
	}
	if maximum < minimum {
		return errors.New("jitter max must be greater than or equal to min")
	}
	return nil
}
