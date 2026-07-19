package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

type Config struct {
	Database DatabaseConfig  `json:"database"`
	Adapters []AdapterConfig `json:"adapters"`
	Profiles []Profile       `json:"profiles"`
	Searches []Search        `json:"searches"`
	Server   ServerConfig    `json:"server,omitempty"`
}

type ServerConfig struct {
	Listen                    string `json:"listen,omitempty"`
	FollowUpReconcileInterval string `json:"follow_up_reconcile_interval,omitempty"`
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
	Tag       string            `json:"tag"`
	Adapter   string            `json:"adapter"`
	StateFile string            `json:"state_file,omitempty"`
	Enabled   bool              `json:"enabled"`
	Bootstrap *ProfileBootstrap `json:"bootstrap,omitempty"`
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
	return nil
}
