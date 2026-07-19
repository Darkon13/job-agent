package config

import (
	"encoding/json"
	"testing"
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
