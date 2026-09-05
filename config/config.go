package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/robfig/cron/v3"
)

type Config struct {
	Database DatabaseConfig  `json:"database"`
	Adapters []AdapterConfig `json:"adapters"`
	Profiles []Profile       `json:"profiles"`
	Searches []Search        `json:"searches"`
	Jobs     []Job           `json:"jobs,omitempty"`
	Server   ServerConfig    `json:"server,omitempty"`
}

const (
	JobActionResumeTouch = "resume.touch"
	JobConcurrencyForbid = "forbid"
)

type Job struct {
	Tag         string       `json:"tag"`
	Enabled     bool         `json:"enabled"`
	Triggers    []JobTrigger `json:"triggers"`
	Concurrency string       `json:"concurrency"`
	Action      JobAction    `json:"action"`
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
	Type    string `json:"type"`
	Profile string `json:"profile"`
	Resume  string `json:"resume,omitempty"`
}

type ServerConfig struct {
	Listen                     string `json:"listen,omitempty"`
	FollowUpReconcileInterval  string `json:"follow_up_reconcile_interval,omitempty"`
	SchedulerReconcileInterval string `json:"scheduler_reconcile_interval,omitempty"`
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
	Tag            string            `json:"tag"`
	Adapter        string            `json:"adapter"`
	Resume         string            `json:"resume,omitempty"`
	CredentialsRef string            `json:"credentials_ref,omitempty"`
	StateFile      string            `json:"state_file,omitempty"`
	Enabled        bool              `json:"enabled"`
	Bootstrap      *ProfileBootstrap `json:"bootstrap,omitempty"`
}

// ProfileBootstrap schedules one idempotent initial profile fill after auth.
// Source is resolved by the config builder and should normally be a mounted
// read-only JSON file.
type ProfileBootstrap struct {
	Source  string `json:"source"`
	When    string `json:"when"`
	Publish bool   `json:"publish,omitempty"`
}

type Search struct {
	Tag                string          `json:"tag"`
	Adapter            string          `json:"adapter"`
	Profiles           []string        `json:"profiles"`
	Priority           int             `json:"priority"`
	TargetApplications int             `json:"target_applications"`
	Fallback           string          `json:"fallback,omitempty"`
	Query              json.RawMessage `json:"query"`
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
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("server listen must use a loopback address until API authentication is implemented")
		}
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
	adapters := make(map[string]struct{}, len(c.Adapters))
	for _, item := range c.Adapters {
		if item.Tag == "" || item.Type == "" {
			return fmt.Errorf("every adapter requires tag and type")
		}
		if _, exists := adapters[item.Tag]; exists {
			return fmt.Errorf("duplicate adapter tag %q", item.Tag)
		}
		adapters[item.Tag] = struct{}{}
	}

	profiles := make(map[string]struct{}, len(c.Profiles))
	for _, profile := range c.Profiles {
		if profile.Tag == "" || profile.Adapter == "" {
			return fmt.Errorf("every profile requires tag and adapter")
		}
		if _, exists := adapters[profile.Adapter]; !exists {
			return fmt.Errorf("profile %q references unknown adapter %q", profile.Tag, profile.Adapter)
		}
		if _, exists := profiles[profile.Tag]; exists {
			return fmt.Errorf("duplicate profile tag %q", profile.Tag)
		}
		if profile.Bootstrap != nil {
			if profile.Bootstrap.Source == "" {
				return fmt.Errorf("profile %q bootstrap requires source", profile.Tag)
			}
			if profile.Bootstrap.When != "empty" && profile.Bootstrap.When != "missing_resume" {
				return fmt.Errorf("profile %q bootstrap when must be empty or missing_resume", profile.Tag)
			}
		}
		profiles[profile.Tag] = struct{}{}
	}

	searches := make(map[string]struct{}, len(c.Searches))
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
		for _, profile := range search.Profiles {
			if _, exists := profiles[profile]; !exists {
				return fmt.Errorf("search %q references unknown profile %q", search.Tag, profile)
			}
		}
		searches[search.Tag] = struct{}{}
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
		if job.Action.Type != JobActionResumeTouch {
			return fmt.Errorf("job %q has unsupported action %q", job.Tag, job.Action.Type)
		}
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
