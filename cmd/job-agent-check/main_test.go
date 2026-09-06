package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/Darkon13/job-agent/config"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

func TestRunReportsBrowserOnlyProfileWithoutConfiguredOperationsAsLaunchable(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "job-agent.db")
	if err := storesqlite.MigrateUp(databasePath); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	statePath := filepath.Join(directory, "browser-state.json")
	if err := os.WriteFile(statePath, []byte(`{"cookies":[{"name":"session","value":"opaque"}]}`), 0o600); err != nil {
		t.Fatalf("write browser state: %v", err)
	}
	configPath := writeConfig(t, directory, appconfig.Config{
		Database: appconfig.DatabaseConfig{Driver: "sqlite", Path: databasePath},
		Adapters: []appconfig.AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []appconfig.Profile{{
			Tag: "primary", Adapter: "hh-main", StateFile: statePath, Enabled: true,
		}},
	})
	var output bytes.Buffer
	if err := run(context.Background(), []string{configPath}, &output); err != nil {
		t.Fatalf("preflight: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "api=not_configured") || !strings.Contains(output.String(), "jobs=0/0") {
		t.Fatalf("unexpected output:\n%s", output.String())
	}
}

func TestRunBlocksSearchWithoutAPIAuthorization(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "job-agent.db")
	if err := storesqlite.MigrateUp(databasePath); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	configPath := writeConfig(t, directory, appconfig.Config{
		Database: appconfig.DatabaseConfig{Driver: "sqlite", Path: databasePath},
		Adapters: []appconfig.AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []appconfig.Profile{{Tag: "primary", Adapter: "hh-main", Enabled: true}},
		Searches: []appconfig.Search{{
			Tag: "backend", Adapter: "hh-main", Profiles: []string{"primary"}, Priority: 1,
			TargetApplications: 1, Query: json.RawMessage(`{"source":"global","text":"Backend"}`),
		}},
	})
	var output bytes.Buffer
	err := run(context.Background(), []string{configPath}, &output)
	if err == nil || !strings.Contains(err.Error(), "preflight blocked") {
		t.Fatalf("error = %v, output:\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "BLOCKED search backend") {
		t.Fatalf("unexpected output:\n%s", output.String())
	}
}

func TestValidateBrowserStateRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"cookies":[{"name":"session","value":"opaque"}]}`), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := validateBrowserState(path); err == nil || err.Error() != "insecure_permissions" {
		t.Fatalf("error = %v", err)
	}
}

func writeConfig(t *testing.T, directory string, cfg appconfig.Config) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	path := filepath.Join(directory, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
