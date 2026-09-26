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
	"sort"
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
	// AnswerSets lists reviewed answer block files. They are resolved relative
	// to the config file and assembled into one runtime registry.
	AnswerSets []string `json:"answer_sets,omitempty"`
	// Include lists additional config files or globs. Included files contribute
	// collections only; database and server stay in the main file.
	Include []string `json:"include,omitempty"`
	// SchemaVersion freezes the config schema. Version 1 is implied when the
	// field is absent; unknown versions are rejected instead of guessed.
	SchemaVersion        int `json:"schema_version,omitempty"`
	resolvedAnswerBlocks []core.AnswerBlock
}

// ResolvedAnswerBlocks returns the reviewed answer blocks loaded from
// AnswerSets. Loading validates each file with core.ValidateAnswerBlock.
func (c Config) ResolvedAnswerBlocks() []core.AnswerBlock {
	return append([]core.AnswerBlock(nil), c.resolvedAnswerBlocks...)
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

const (
	ModelProviderOpenAIResponses = "openai_responses"
	ModelProviderOpenAIChat      = "openai_chat"
)

// CurrentConfigSchemaVersion is the frozen v1 config schema. Breaking changes
// require a new version and an explicit migration path instead of silent
// reinterpretation of existing files.
const CurrentConfigSchemaVersion = 1

type ModelProviderConfig struct {
	Tag             string `json:"tag"`
	Type            string `json:"type"`
	Model           string `json:"model"`
	BaseURL         string `json:"base_url,omitempty"`
	APIKeyEnv       string `json:"api_key_env,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
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
	JobActionResumePublish              = "resume.publish"
	JobActionResumeUpdate               = "resume.update"
	JobActionApplicationCampaign        = "application.campaign"
	JobActionProfileStateReconcile      = "profile_state.reconcile"
	JobActionProfileActivityObserve     = "profile.activity.observe"
	JobActionProfileActivityMaintain    = "profile.activity.maintain"
	JobActionApplicationValidationCheck = "application.validation.refresh"
	JobActionProfileSessionRefresh      = "profile.session_refresh"
	JobActionConversationSync           = "conversation.sync"
	JobActionConversationFollowUpSelect = "conversation.follow_up.select"
	JobActionApplicationRetention       = "application.retention"
	JobActionApplicationStateSync       = "application.state.sync"
	JobActionApplicationAnswerCovered   = "application.answer_covered"
	JobConcurrencyForbid                = "forbid"
	ApplicationValidationReview         = "review"
	ApplicationValidationSkip           = "skip"
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
	Tag string `json:"tag"`
	// Description is an optional human-readable note shown in the dashboard.
	Description string            `json:"description,omitempty"`
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
	Publish          bool                                 `json:"publish,omitempty"`
	Profiles         []string                             `json:"profiles,omitempty"`
	Routes           []string                             `json:"routes,omitempty"`
	TargetSuccessful int                                  `json:"target_successful,omitempty"`
	MaxInFlight      int                                  `json:"max_in_flight,omitempty"`
	FollowUp         *ConversationFollowUpSelectionConfig `json:"follow_up,omitempty"`
	Count            int                                  `json:"count,omitempty"`
	Pause            core.Duration                        `json:"pause,omitempty"`
	// MinAge delays the validation refresh so fresh questionnaires keep their
	// operator window before the recheck.
	MinAge    core.Duration               `json:"min_age,omitempty"`
	Retention *ApplicationRetentionConfig `json:"retention,omitempty"`
}

// TargetProfiles returns the profile tags a job action applies to. A job either
// names one profile or lists several with the same schedule; list jobs expand
// into one scheduler entry per profile.
func (action JobAction) TargetProfiles() []string {
	if len(action.Profiles) > 0 {
		return append([]string(nil), action.Profiles...)
	}
	if action.Profile != "" {
		return []string{action.Profile}
	}
	return nil
}

type ApplicationRetentionConfig struct {
	StaleAfter              core.Duration `json:"stale_after"`
	RemoveRejected          *bool         `json:"remove_rejected,omitempty"`
	RemoveWaitingValidation bool          `json:"remove_waiting_validation,omitempty"`
	// ValidationStaleAfter overrides StaleAfter for questionnaires that never
	// became applications. Zero keeps the shared window.
	ValidationStaleAfter core.Duration `json:"validation_stale_after,omitempty"`
}

func (configured ApplicationRetentionConfig) Payload(profileID core.ProfileID) core.ApplicationRetentionPayload {
	removeRejected := true
	if configured.RemoveRejected != nil {
		removeRejected = *configured.RemoveRejected
	}
	return core.ApplicationRetentionPayload{
		ProfileID: profileID, StaleAfter: configured.StaleAfter, RemoveRejected: removeRejected,
		RemoveWaitingValidation: configured.RemoveWaitingValidation,
		ValidationStaleAfter:    configured.ValidationStaleAfter,
	}
}

type ConversationFollowUpSelectionConfig struct {
	Strategy       core.FollowUpSelectionStrategy `json:"strategy"`
	MinimumSilence core.Duration                  `json:"minimum_silence"`
	RunAfter       core.Duration                  `json:"run_after"`
	DeadlineAfter  core.Duration                  `json:"deadline_after,omitempty"`
	Content        core.MessageContent            `json:"content"`
	Policy         core.FollowUpPolicy            `json:"policy"`
	Limit          int                            `json:"limit,omitempty"`
	All            bool                           `json:"all,omitempty"`
}

func (configured ConversationFollowUpSelectionConfig) Payload(profileID core.ProfileID) core.ConversationFollowUpSelectPayload {
	return core.ConversationFollowUpSelectPayload{
		ProfileID: profileID, Strategy: configured.Strategy, MinimumSilence: configured.MinimumSilence,
		RunAfter: configured.RunAfter, DeadlineAfter: configured.DeadlineAfter,
		Content: configured.Content, Policy: configured.Policy,
		Limit: configured.Limit, All: configured.All,
	}
}

type ServerConfig struct {
	Listen                     string `json:"listen,omitempty"`
	Exposure                   string `json:"exposure,omitempty"`
	APITokenEnv                string `json:"api_token_env,omitempty"`
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
	ResumeAliases       map[string]string  `json:"resume_aliases,omitempty"`
	ResumeFactsFile     string             `json:"resume_facts_file,omitempty"`
	CredentialsRef      string             `json:"credentials_ref,omitempty"`
	StateFile           string             `json:"state_file,omitempty"`
	Enabled             bool               `json:"enabled"`
	Bootstrap           *ProfileBootstrap  `json:"bootstrap,omitempty"`
	Applications        ApplicationPolicy  `json:"applications,omitempty"`
	Conversations       ConversationPolicy `json:"conversations,omitempty"`
	Answers             *AnswerPolicy      `json:"answers,omitempty"`
	Contacts            *ProfileContacts   `json:"contacts,omitempty"`
	resolvedResumeFacts *ApplicationResumeFacts
}

// ProfileContacts are sender contacts rendered into letters and masked for
// model calls. They are not secrets, but they are personal data.
type ProfileContacts struct {
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Email     string `json:"email,omitempty"`
	Telegram  string `json:"telegram,omitempty"`
}

func (contacts ProfileContacts) validate() error {
	values := map[string]string{
		"first_name": contacts.FirstName, "last_name": contacts.LastName,
		"email": contacts.Email, "telegram": contacts.Telegram,
	}
	nonEmpty := 0
	for name, value := range values {
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("profile contacts %s must not contain line breaks", name)
		}
		if strings.TrimSpace(value) != "" {
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		return errors.New("profile contacts must set at least one of first_name, last_name, email, telegram")
	}
	if email := strings.TrimSpace(contacts.Email); email != "" && !strings.Contains(email, "@") {
		return errors.New("profile contacts email must contain @")
	}
	return nil
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
	// AnswerKnown replies to a questionnaire prompt only when a reviewed
	// conversation answer selects one of the offered text buttons.
	AnswerKnown bool `json:"answer_known,omitempty"`
}

// AnswerPolicy configures how unknown questions are resolved after the
// reviewed answer block. The model mode submits a locally validated answer and
// records its provenance; there is no approval step for qualification attempts.
type AnswerPolicy struct {
	Model *AnswerModelPolicy `json:"model,omitempty"`
}

type AnswerModelPolicy struct {
	Provider      string `json:"provider"`
	PromptVersion string `json:"prompt_version"`
	Instruction   string `json:"instruction"`
	Timeout       string `json:"timeout"`
}

func (policy AnswerModelPolicy) TimeoutDuration() (time.Duration, error) {
	return time.ParseDuration(strings.TrimSpace(policy.Timeout))
}

type ApplicationPolicy struct {
	Mode                  string                    `json:"mode,omitempty"`
	Message               string                    `json:"message,omitempty"`
	MessageTemplate       string                    `json:"message_template,omitempty"`
	MessageTemplateFile   string                    `json:"message_template_file,omitempty"`
	Model                 *ApplicationModelPolicy   `json:"model,omitempty"`
	EmployerRules         []ApplicationEmployerRule `json:"employer_rules,omitempty"`
	Qualification         ApplicationQualification  `json:"qualification,omitempty"`
	DailyLimit            int                       `json:"daily_limit,omitempty"`
	SubmitJitter          JitterConfig              `json:"submit_jitter,omitempty"`
	Timezone              string                    `json:"timezone,omitempty"`
	AllowVisibilityChange bool                      `json:"allow_visibility_change,omitempty"`
	// ValidationAction decides what happens when a vacancy requires a
	// questionnaire or test: "review" waits for the operator, "skip" marks the
	// application skipped so it can be retried after the form is filled.
	ValidationAction    string                      `json:"validation_action,omitempty"`
	Tailoring           *ApplicationTailoringPolicy `json:"tailoring,omitempty"`
	resolvedTemplate    string
	resolvedMessagePool *ApplicationMessagePool
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
	IncludeAll []string `json:"include_all,omitempty"`
	ExcludeAny []string `json:"exclude_any,omitempty"`
	ExcludeAll []string `json:"exclude_all,omitempty"`
}

type ApplicationTailoringPolicy struct {
	Skills     *ApplicationTailoringSkillsPolicy     `json:"skills,omitempty"`
	About      *ApplicationTailoringAboutPolicy      `json:"about,omitempty"`
	Experience *ApplicationTailoringExperiencePolicy `json:"experience,omitempty"`
}

type ApplicationTailoringSkillsPolicy struct {
	Enabled       bool                    `json:"enabled,omitempty"`
	Maximum       int                     `json:"maximum,omitempty"`
	AllowRemovals bool                    `json:"allow_removals,omitempty"`
	Model         *ApplicationModelPolicy `json:"model,omitempty"`
	Write         []string                `json:"write,omitempty"`
	Readonly      []string                `json:"readonly,omitempty"`
}

// SkipValidation reports whether questionnaire and test vacancies should be
// skipped instead of waiting for operator input.
func (policy ApplicationPolicy) SkipValidation() bool {
	return strings.TrimSpace(policy.ValidationAction) == ApplicationValidationSkip
}

func (policy ApplicationPolicy) TailoringSkills() (ApplicationTailoringSkillsPolicy, bool) {
	if policy.Tailoring == nil || policy.Tailoring.Skills == nil || !policy.Tailoring.Skills.Enabled {
		return ApplicationTailoringSkillsPolicy{}, false
	}
	return *policy.Tailoring.Skills, true
}

// ApplicationTailoringAboutPolicy rewrites the resume "About" section for the
// current vacancy. It requires a model and resume facts.
type ApplicationTailoringAboutPolicy struct {
	Enabled      bool                    `json:"enabled,omitempty"`
	MaximumRunes int                     `json:"maximum_runes,omitempty"`
	Model        *ApplicationModelPolicy `json:"model,omitempty"`
	Write        []string                `json:"write,omitempty"`
	Readonly     []string                `json:"readonly,omitempty"`
}

func (policy ApplicationPolicy) TailoringAbout() (ApplicationTailoringAboutPolicy, bool) {
	if policy.Tailoring == nil || policy.Tailoring.About == nil || !policy.Tailoring.About.Enabled {
		return ApplicationTailoringAboutPolicy{}, false
	}
	return *policy.Tailoring.About, true
}

// ApplicationTailoringExperiencePolicy rewrites work experience descriptions
// for the current vacancy. It requires a model.
type ApplicationTailoringExperiencePolicy struct {
	Enabled      bool                                  `json:"enabled,omitempty"`
	MaximumRunes int                                   `json:"maximum_runes,omitempty"`
	Model        *ApplicationModelPolicy               `json:"model,omitempty"`
	Write        []string                              `json:"write,omitempty"`
	Readonly     []string                              `json:"readonly,omitempty"`
	Groups       []ApplicationTailoringExperienceGroup `json:"groups,omitempty"`
}

// ApplicationTailoringExperienceGroup is one model request inside the
// experience step: its own blocks, instruction and optional context.
type ApplicationTailoringExperienceGroup struct {
	Write        []string                `json:"write,omitempty"`
	Readonly     []string                `json:"readonly,omitempty"`
	MaximumRunes int                     `json:"maximum_runes,omitempty"`
	Model        *ApplicationModelPolicy `json:"model,omitempty"`
}

func (policy ApplicationPolicy) TailoringExperience() (ApplicationTailoringExperiencePolicy, bool) {
	if policy.Tailoring == nil || policy.Tailoring.Experience == nil || !policy.Tailoring.Experience.Enabled {
		return ApplicationTailoringExperiencePolicy{}, false
	}
	return *policy.Tailoring.Experience, true
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
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve config path %q: %w", path, err)
	}
	cfg, err := loadConfigFile(absolute, make(map[string]bool), 0)
	if err != nil {
		return Config{}, err
	}
	if err := cfg.resolveResumeAliases(); err != nil {
		return Config{}, err
	}
	baseDirectory := filepath.Dir(absolute)
	if err := cfg.resolveApplicationMessageFiles(baseDirectory); err != nil {
		return Config{}, err
	}
	if err := cfg.resolveApplicationResumeFactsFiles(baseDirectory); err != nil {
		return Config{}, err
	}
	if err := cfg.resolveProfileBootstrapFiles(baseDirectory); err != nil {
		return Config{}, err
	}
	if err := cfg.resolveAnswerSets(baseDirectory); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %s: %w", absolute, err)
	}
	return cfg, nil
}

const maximumConfigIncludeDepth = 16

// resolveResumeAliases replaces user-facing resume aliases with platform resume
// IDs. profile.resume may be an alias; a job action resolves through its
// explicit profile.
func (c *Config) resolveResumeAliases() error {
	byProfile := make(map[core.ProfileID]map[string]string, len(c.Profiles))
	for index := range c.Profiles {
		profile := &c.Profiles[index]
		if len(profile.ResumeAliases) == 0 {
			continue
		}
		clean := make(map[string]string, len(profile.ResumeAliases))
		for alias, id := range profile.ResumeAliases {
			alias = strings.TrimSpace(alias)
			id = strings.TrimSpace(id)
			if alias == "" || id == "" {
				return fmt.Errorf("profile %q resume_aliases requires non-empty alias and resume id", profile.Tag)
			}
			clean[alias] = id
		}
		profile.ResumeAliases = clean
		profileID := core.ProfileID(profile.Tag)
		byProfile[profileID] = clean
		if resolved, exists := clean[strings.TrimSpace(profile.Resume)]; exists {
			profile.Resume = resolved
		}
	}
	for index := range c.Jobs {
		job := &c.Jobs[index]
		reference := strings.TrimSpace(job.Action.Resume)
		if reference == "" {
			continue
		}
		profileID := core.ProfileID(strings.TrimSpace(job.Action.Profile))
		if resolved, exists := byProfile[profileID][reference]; exists {
			job.Action.Resume = resolved
		}
	}
	return nil
}

// ResumeTargets builds the per-profile resume catalog for the control API.
func (c Config) ResumeTargets() map[core.ProfileID][]core.ResumeTarget {
	targets := make(map[core.ProfileID][]core.ResumeTarget, len(c.Profiles))
	for _, profile := range c.Profiles {
		profileID := core.ProfileID(profile.Tag)
		list := make([]core.ResumeTarget, 0, len(profile.ResumeAliases)+1)
		if id := strings.TrimSpace(profile.Resume); id != "" {
			list = append(list, core.ResumeTarget{ID: id, Primary: true})
		}
		names := make([]string, 0, len(profile.ResumeAliases))
		for alias := range profile.ResumeAliases {
			names = append(names, alias)
		}
		sort.Strings(names)
		for _, alias := range names {
			list = append(list, core.ResumeTarget{ID: profile.ResumeAliases[alias], Alias: alias})
		}
		targets[profileID] = list
	}
	return targets
}

// loadConfigFile reads one config file, merges its includes depth-first, and
// returns the combined object. The main file owns database and server; every
// declared file contributes adapters, profiles, searches, resources, employer
// groups, models, jobs and answer sets. File references inside a file resolve
// relative to that file, so included fragments stay movable.
func loadConfigFile(path string, visiting map[string]bool, depth int) (Config, error) {
	if depth > maximumConfigIncludeDepth {
		return Config{}, fmt.Errorf("config %s: include depth exceeds %d", path, maximumConfigIncludeDepth)
	}
	if visiting[path] {
		return Config{}, fmt.Errorf("config %s: include cycle detected", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	var file Config
	if err := json.Unmarshal(data, &file); err != nil {
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	if depth > 0 {
		if _, exists := raw["database"]; exists {
			return Config{}, fmt.Errorf("config %s: database is only allowed in the main file", path)
		}
		if _, exists := raw["server"]; exists {
			return Config{}, fmt.Errorf("config %s: server is only allowed in the main file", path)
		}
		if _, exists := raw["schema_version"]; exists {
			return Config{}, fmt.Errorf("config %s: schema_version is only allowed in the main file", path)
		}
	}
	directory := filepath.Dir(path)
	normalizeConfigFileReferences(&file, directory)
	result := mergeConfigCollections(Config{Database: file.Database, Server: file.Server, SchemaVersion: file.SchemaVersion}, file)
	visiting[path] = true
	defer delete(visiting, path)
	for _, reference := range file.Include {
		matches, err := expandConfigIncludes(directory, reference)
		if err != nil {
			return Config{}, err
		}
		for _, match := range matches {
			included, err := loadConfigFile(match, visiting, depth+1)
			if err != nil {
				return Config{}, err
			}
			result = mergeConfigCollections(result, included)
		}
	}
	return result, nil
}

func mergeConfigCollections(target, extra Config) Config {
	target.Adapters = append(target.Adapters, extra.Adapters...)
	target.Profiles = append(target.Profiles, extra.Profiles...)
	target.Searches = append(target.Searches, extra.Searches...)
	target.Resources = append(target.Resources, extra.Resources...)
	target.EmployerGroups = append(target.EmployerGroups, extra.EmployerGroups...)
	target.Models = append(target.Models, extra.Models...)
	target.Jobs = append(target.Jobs, extra.Jobs...)
	target.AnswerSets = append(target.AnswerSets, extra.AnswerSets...)
	return target
}

func normalizeConfigFileReferences(cfg *Config, directory string) {
	for index := range cfg.AnswerSets {
		cfg.AnswerSets[index] = configFileReference(directory, cfg.AnswerSets[index])
	}
	for index := range cfg.Profiles {
		profile := &cfg.Profiles[index]
		profile.ResumeFactsFile = configFileReference(directory, profile.ResumeFactsFile)
		profile.Applications.MessageTemplateFile = configFileReference(directory, profile.Applications.MessageTemplateFile)
		for ruleIndex := range profile.Applications.EmployerRules {
			rule := &profile.Applications.EmployerRules[ruleIndex]
			rule.MessageTemplateFile = configFileReference(directory, rule.MessageTemplateFile)
		}
		if profile.Bootstrap != nil {
			profile.Bootstrap.Source = configFileReference(directory, profile.Bootstrap.Source)
		}
	}
}

func configFileReference(directory, reference string) string {
	reference = strings.TrimSpace(reference)
	if reference == "" || filepath.IsAbs(reference) {
		return reference
	}
	return filepath.Clean(filepath.Join(directory, reference))
}

func expandConfigIncludes(directory, reference string) ([]string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return nil, errors.New("include path must not be empty")
	}
	pattern := reference
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(directory, pattern)
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return []string{filepath.Clean(pattern)}, nil
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("include %q: %w", reference, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("include %q matched no files", reference)
	}
	sort.Strings(matches)
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		result = append(result, filepath.Clean(match))
	}
	return result, nil
}

func (c *Config) resolveAnswerSets(baseDirectory string) error {
	for _, reference := range c.AnswerSets {
		reference = strings.TrimSpace(reference)
		if reference == "" {
			return errors.New("answer_sets contains an empty path")
		}
		path := reference
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDirectory, path)
		}
		block, err := LoadAnswerBlock(path)
		if err != nil {
			return fmt.Errorf("answer_sets %q: %w", reference, err)
		}
		// Duplicate tags or ambiguous matchers are rejected when the runtime
		// registry is built, not silently merged here.
		if tag := block.Tag; strings.TrimSpace(tag) == "" {
			return fmt.Errorf("answer_sets %q: block tag is required", reference)
		}
		c.resolvedAnswerBlocks = append(c.resolvedAnswerBlocks, block)
	}
	return nil
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
	if config.Type != ModelProviderOpenAIResponses && config.Type != ModelProviderOpenAIChat {
		return fmt.Errorf("model provider %q has unsupported type %q", config.Tag, config.Type)
	}
	if environment := config.APIKeyEnvironment(); strings.Contains(environment, "=") || strings.ContainsAny(environment, " \t\r\n") {
		return fmt.Errorf("model provider %q has invalid api_key_env", config.Tag)
	}
	if config.MaxOutputTokens < 0 || config.MaxOutputTokens > 32768 {
		return fmt.Errorf("model provider %q max_output_tokens must be between 1 and 32768 when set", config.Tag)
	}
	if effort := strings.TrimSpace(config.ReasoningEffort); effort != "" {
		switch effort {
		case "none", "low", "medium", "high":
		default:
			return fmt.Errorf("model provider %q reasoning_effort must be none, low, medium or high", config.Tag)
		}
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

func validateResumeTailoringObjects(processor string, write, readonly []string) error {
	for _, value := range write {
		ref, err := applicationoperator.ParseResumeTailoringObject(value)
		if err != nil {
			return err
		}
		if err := applicationoperator.ValidateResumeTailoringWriteObject(processor, ref); err != nil {
			return err
		}
	}
	for _, value := range readonly {
		ref, err := applicationoperator.ParseResumeTailoringObject(value)
		if err != nil {
			return err
		}
		if err := applicationoperator.ValidateResumeTailoringReadObject(ref); err != nil {
			return err
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

func validateAnswerModelPolicy(label string, policy *AnswerModelPolicy, providers map[string]struct{}) error {
	if policy == nil {
		return nil
	}
	if _, exists := providers[strings.TrimSpace(policy.Provider)]; !exists {
		return fmt.Errorf("%s references unknown model provider %q", label, policy.Provider)
	}
	if strings.TrimSpace(policy.PromptVersion) == "" || strings.TrimSpace(policy.Instruction) == "" {
		return fmt.Errorf("%s model requires prompt_version and instruction", label)
	}
	timeout, err := policy.TimeoutDuration()
	if err != nil || timeout <= 0 || timeout > 5*time.Minute {
		return fmt.Errorf("%s model timeout must be a positive duration no greater than 5m", label)
	}
	return nil
}

func (c Config) Validate() error {
	if c.SchemaVersion != 0 && c.SchemaVersion != CurrentConfigSchemaVersion {
		return fmt.Errorf("config schema_version %d is not supported (current %d)", c.SchemaVersion, CurrentConfigSchemaVersion)
	}
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
		if host == "localhost" || c.Server.APITokenEnv != "" {
			break
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("server listen must use a loopback address when exposure is %q unless api_token_env is set", ServerExposureLoopback)
		}
	case ServerExposurePrivate:
		// Private exposure is intended only for an un-published container network.
		// The separately deployed dashboard is the single ingress endpoint.
	default:
		return fmt.Errorf("server exposure must be %q or %q", ServerExposureLoopback, ServerExposurePrivate)
	}
	if value := strings.TrimSpace(c.Server.APITokenEnv); value != "" && strings.ContainsAny(value, " \t=") {
		return fmt.Errorf("server api_token_env must be an environment variable name")
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
		if profile.Conversations.AnswerKnown && !profile.Conversations.AllowSend {
			return fmt.Errorf("profile %q conversations.answer_known requires allow_send", profile.Tag)
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
		switch strings.TrimSpace(profile.Applications.ValidationAction) {
		case "", ApplicationValidationReview, ApplicationValidationSkip:
		default:
			return fmt.Errorf("profile %q application validation_action must be %q or %q", profile.Tag, ApplicationValidationReview, ApplicationValidationSkip)
		}
		if profile.Contacts != nil {
			if err := profile.Contacts.validate(); err != nil {
				return fmt.Errorf("profile %q contacts: %w", profile.Tag, err)
			}
		}
		skills, skillsEnabled := profile.Applications.TailoringSkills()
		about, aboutEnabled := profile.Applications.TailoringAbout()
		experience, experienceEnabled := profile.Applications.TailoringExperience()
		if skillsEnabled || aboutEnabled || experienceEnabled {
			if profile.Applications.ExecutionMode() != ApplicationModeSubmit {
				return fmt.Errorf("profile %q application tailoring requires submit mode", profile.Tag)
			}
			if strings.TrimSpace(profile.Resume) == "" {
				return fmt.Errorf("profile %q application tailoring requires a resume", profile.Tag)
			}
		}
		if skillsEnabled {
			if skills.Maximum < 1 {
				return fmt.Errorf("profile %q application tailoring skills require a positive maximum", profile.Tag)
			}
			if err := validateResumeTailoringObjects("skills", skills.Write, skills.Readonly); err != nil {
				return fmt.Errorf("profile %q application tailoring skills: %w", profile.Tag, err)
			}
			if skills.Model != nil {
				if err := validateApplicationModelPolicy(fmt.Sprintf("profile %q application tailoring", profile.Tag), skills.Model, modelProviders); err != nil {
					return err
				}
			}
		}
		if aboutEnabled {
			if about.Model == nil {
				return fmt.Errorf("profile %q application tailoring about requires a model", profile.Tag)
			}
			if about.MaximumRunes < 1 {
				return fmt.Errorf("profile %q application tailoring about requires a positive maximum_runes", profile.Tag)
			}
			if facts, exists := profile.ResolvedResumeFacts(); exists && facts.ResumeID != profile.Resume {
				return fmt.Errorf("profile %q application tailoring about resume facts must describe the profile resume", profile.Tag)
			}
			if err := validateApplicationModelPolicy(fmt.Sprintf("profile %q application tailoring", profile.Tag), about.Model, modelProviders); err != nil {
				return err
			}
			if err := validateResumeTailoringObjects("about", about.Write, about.Readonly); err != nil {
				return fmt.Errorf("profile %q application tailoring about: %w", profile.Tag, err)
			}
		}
		if experienceEnabled {
			if len(experience.Groups) > 0 {
				if experience.Model != nil || len(experience.Write) > 0 || len(experience.Readonly) > 0 {
					return fmt.Errorf("profile %q application tailoring experience cannot mix groups with model/write/readonly", profile.Tag)
				}
				targets := make(map[string]struct{})
				for index, group := range experience.Groups {
					label := fmt.Sprintf("profile %q application tailoring experience group %d", profile.Tag, index)
					if group.Model == nil {
						return fmt.Errorf("%s requires a model", label)
					}
					if group.MaximumRunes < 0 {
						return fmt.Errorf("%s has a negative maximum_runes", label)
					}
					if err := validateApplicationModelPolicy(label, group.Model, modelProviders); err != nil {
						return err
					}
					if err := validateResumeTailoringObjects("experience", group.Write, group.Readonly); err != nil {
						return fmt.Errorf("%s: %w", label, err)
					}
					if len(group.Write) == 0 {
						return fmt.Errorf("%s requires at least one write object", label)
					}
					for _, value := range group.Write {
						ref, _ := applicationoperator.ParseResumeTailoringObject(value)
						key := ref.String()
						if _, duplicate := targets[key]; duplicate {
							return fmt.Errorf("%s targets %q which another group also writes", label, key)
						}
						targets[key] = struct{}{}
					}
				}
			} else {
				if experience.Model == nil {
					return fmt.Errorf("profile %q application tailoring experience requires a model", profile.Tag)
				}
				if experience.MaximumRunes < 1 {
					return fmt.Errorf("profile %q application tailoring experience requires a positive maximum_runes", profile.Tag)
				}
				if err := validateApplicationModelPolicy(fmt.Sprintf("profile %q application tailoring", profile.Tag), experience.Model, modelProviders); err != nil {
					return err
				}
				if err := validateResumeTailoringObjects("experience", experience.Write, experience.Readonly); err != nil {
					return fmt.Errorf("profile %q application tailoring experience: %w", profile.Tag, err)
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
		if profile.Answers != nil {
			if err := validateAnswerModelPolicy(fmt.Sprintf("profile %q answers", profile.Tag), profile.Answers.Model, modelProviders); err != nil {
				return err
			}
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
			"include_all": profile.Applications.Qualification.IncludeAll,
			"exclude_any": profile.Applications.Qualification.ExcludeAny,
			"exclude_all": profile.Applications.Qualification.ExcludeAll,
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
		if _, err := c.ResolveSearchChain(search.Tag); err != nil {
			return err
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
		if job.Action.Profile != "" && len(job.Action.Profiles) > 0 {
			return fmt.Errorf("job %q action cannot set both profile and profiles", job.Tag)
		}
		if len(job.Action.Profiles) > 0 && (job.Action.Type == JobActionResumeUpdate || job.Action.Type == JobActionProfileStateReconcile) {
			return fmt.Errorf("job %q action %s does not accept profiles", job.Tag, job.Action.Type)
		}
		switch job.Action.Type {
		case JobActionProfileSessionRefresh:
			targets, err := jobActionTargets(job, profiles)
			if err != nil {
				return err
			}
			for _, target := range targets {
				if strings.TrimSpace(profileConfigs[target].StateFile) == "" {
					return fmt.Errorf("job %q profile.session_refresh requires a profile state_file", job.Tag)
				}
			}
		case JobActionResumeTouch, JobActionResumePublish, JobActionProfileActivityObserve:
			targets, err := jobActionTargets(job, profiles)
			if err != nil {
				return err
			}
			for _, target := range targets {
				resume := job.Action.Resume
				if resume == "" {
					resume = profileConfigs[target].Resume
				}
				if resume == "" {
					return fmt.Errorf("job %q %s requires resume", job.Tag, job.Action.Type)
				}
			}
		case JobActionResumeUpdate:
			if _, exists := profiles[job.Action.Profile]; !exists {
				return fmt.Errorf("job %q references unknown profile %q", job.Tag, job.Action.Profile)
			}
			resourceTag := strings.TrimSpace(job.Action.Resource)
			if resourceTag == "" {
				return fmt.Errorf("job %q resume.update requires resource", job.Tag)
			}
			resourceProfile, exists := resources[resourceTag]
			if !exists {
				return fmt.Errorf("job %q references unknown profile state resource %q", job.Tag, job.Action.Resource)
			}
			if resourceProfile != core.ProfileID(job.Action.Profile) {
				return fmt.Errorf("job %q resource %q belongs to another profile", job.Tag, resourceTag)
			}
			if job.Action.Publish {
				resume := job.Action.Resume
				if resume == "" {
					resume = profileConfigs[job.Action.Profile].Resume
				}
				if resume == "" {
					return fmt.Errorf("job %q resume.update with publish requires resume", job.Tag)
				}
			}
		case JobActionConversationSync:
			if _, err := jobActionTargets(job, profiles); err != nil {
				return err
			}
		case JobActionConversationFollowUpSelect:
			targets, err := jobActionTargets(job, profiles)
			if err != nil {
				return err
			}
			if job.Action.FollowUp == nil {
				return fmt.Errorf("job %q requires follow_up selection settings", job.Tag)
			}
			for _, target := range targets {
				if !profileConfigs[target].Conversations.AllowSend {
					return fmt.Errorf("job %q requires conversations.allow_send for profile %q", job.Tag, target)
				}
				if err := job.Action.FollowUp.Payload(core.ProfileID(target)).Validate(); err != nil {
					return fmt.Errorf("job %q follow_up: %w", job.Tag, err)
				}
			}
		case JobActionProfileActivityMaintain:
			targets, err := jobActionTargets(job, profiles)
			if err != nil {
				return err
			}
			if job.Action.Count < 1 || job.Action.Count > 50 {
				return fmt.Errorf("job %q profile.activity.maintain count must be between 1 and 50", job.Tag)
			}
			if job.Action.Pause.Value() < 0 {
				return fmt.Errorf("job %q profile.activity.maintain pause must not be negative", job.Tag)
			}
			_ = targets
		case JobActionApplicationValidationCheck:
			if _, err := jobActionTargets(job, profiles); err != nil {
				return err
			}
			if job.Action.Count < 1 || job.Action.Count > 200 {
				return fmt.Errorf("job %q application.validation.refresh count must be between 1 and 200", job.Tag)
			}
			if job.Action.MinAge.Value() < 0 {
				return fmt.Errorf("job %q application.validation.refresh min_age must not be negative", job.Tag)
			}
		case JobActionApplicationStateSync:
			if _, err := jobActionTargets(job, profiles); err != nil {
				return err
			}
		case JobActionApplicationAnswerCovered:
			if _, err := jobActionTargets(job, profiles); err != nil {
				return err
			}
			if job.Action.Count < 0 || job.Action.Count > 200 {
				return fmt.Errorf("job %q application.answer_covered count must be between 0 and 200", job.Tag)
			}
		case JobActionApplicationRetention:
			targets, err := jobActionTargets(job, profiles)
			if err != nil {
				return err
			}
			if job.Action.Retention == nil {
				return fmt.Errorf("job %q requires application retention settings", job.Tag)
			}
			for _, target := range targets {
				if err := job.Action.Retention.Payload(core.ProfileID(target)).Validate(); err != nil {
					return fmt.Errorf("job %q retention: %w", job.Tag, err)
				}
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

// ResolveSearchChain expands an explicit fallback chain: the search itself,
// then its fallback, and so on. The order is the order a campaign follows when
// a route is exhausted. Cycles and unknown references are rejected.
func (c Config) ResolveSearchChain(tag string) ([]string, error) {
	byTag := make(map[string]Search, len(c.Searches))
	for _, search := range c.Searches {
		byTag[search.Tag] = search
	}
	chain := make([]string, 0, 4)
	seen := make(map[string]struct{})
	current := strings.TrimSpace(tag)
	for current != "" {
		if _, duplicate := seen[current]; duplicate {
			return nil, fmt.Errorf("search fallback chain %q contains a cycle", tag)
		}
		search, exists := byTag[current]
		if !exists {
			return nil, fmt.Errorf("search %q references unknown fallback %q", tag, current)
		}
		seen[current] = struct{}{}
		chain = append(chain, current)
		current = strings.TrimSpace(search.Fallback)
	}
	return chain, nil
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

func jobActionTargets(job Job, profiles map[string]struct{}) ([]string, error) {
	targets := job.Action.TargetProfiles()
	if len(targets) == 0 {
		return nil, fmt.Errorf("job %q action requires profile or profiles", job.Tag)
	}
	for _, target := range targets {
		if _, exists := profiles[target]; !exists {
			return nil, fmt.Errorf("job %q references unknown profile %q", job.Tag, target)
		}
	}
	return targets, nil
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
