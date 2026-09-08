package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapters/hh"
	"github.com/Darkon13/job-agent/broker"
	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

func TestRunReportsBrowserOnlyProfileWithoutConfiguredOperationsAsLaunchable(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "job-agent.db")
	if err := storesqlite.MigrateUp(databasePath); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	statePath := filepath.Join(directory, "browser-state.json")
	if err := os.WriteFile(statePath, []byte(`{"cookies":[{"name":"session","value":"opaque","domain":".hh.ru"}]}`), 0o600); err != nil {
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

func TestRunAllowsDryRunSearchWithHHBrowserState(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "job-agent.db")
	if err := storesqlite.MigrateUp(databasePath); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	statePath := filepath.Join(directory, "browser-state.json")
	if err := os.WriteFile(statePath, []byte(`{"cookies":[{"name":"session","value":"opaque","domain":".hh.ru"}]}`), 0o600); err != nil {
		t.Fatalf("write browser state: %v", err)
	}
	configPath := writeConfig(t, directory, appconfig.Config{
		Database: appconfig.DatabaseConfig{Driver: "sqlite", Path: databasePath},
		Adapters: []appconfig.AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []appconfig.Profile{{Tag: "primary", Adapter: "hh-main", StateFile: statePath, Enabled: true}},
		Searches: []appconfig.Search{{
			Tag: "backend", Adapter: "hh-main", Profiles: []string{"primary"}, Priority: 1,
			TargetApplications: 1, Query: json.RawMessage(`{"source":"global","text":"Backend","max_pages":1}`),
		}},
	})
	var output bytes.Buffer
	if err := run(context.Background(), []string{configPath}, &output); err != nil {
		t.Fatalf("preflight: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "searches=1/1") {
		t.Fatalf("unexpected output:\n%s", output.String())
	}
}

func TestRunAllowsSubmitCampaignWithHHBrowserState(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "job-agent.db")
	if err := storesqlite.MigrateUp(databasePath); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	statePath := filepath.Join(directory, "browser-state.json")
	if err := os.WriteFile(statePath, []byte(`{"cookies":[{"name":"session","value":"opaque","domain":".hh.ru"}]}`), 0o600); err != nil {
		t.Fatalf("write browser state: %v", err)
	}
	configPath := writeConfig(t, directory, appconfig.Config{
		Database: appconfig.DatabaseConfig{Driver: "sqlite", Path: databasePath},
		Adapters: []appconfig.AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []appconfig.Profile{{
			Tag: "primary", Adapter: "hh-main", StateFile: statePath, Resume: "resume-1", Enabled: true,
			Applications: appconfig.ApplicationPolicy{Mode: appconfig.ApplicationModeSubmit, DailyLimit: 3, Timezone: "Europe/Moscow", Message: "Здравствуйте!"},
		}},
		Searches: []appconfig.Search{{
			Tag: "backend", Adapter: "hh-main", Profiles: []string{"primary"}, Priority: 1,
			TargetApplications: 1, Query: json.RawMessage(`{"source":"global","text":"Backend","max_pages":1}`),
		}},
		Jobs: []appconfig.Job{{
			Tag: "applications", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
			Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "0 9 * * *", Timezone: "Europe/Moscow", Misfire: "run_once"}},
			Action: appconfig.JobAction{
				Type: appconfig.JobActionApplicationCampaign, Profiles: []string{"primary"}, Routes: []string{"backend"},
				TargetSuccessful: 1, MaxInFlight: 1,
			},
		}},
	})
	var output bytes.Buffer
	if err := run(context.Background(), []string{configPath}, &output); err != nil {
		t.Fatalf("preflight: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "searches=1/1") || !strings.Contains(output.String(), "jobs=1/1") {
		t.Fatalf("unexpected output:\n%s", output.String())
	}
}

func TestRunReportsHistoricalFailureAsDegradedWithoutExposingPayload(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "job-agent.db")
	if err := storesqlite.MigrateUp(databasePath); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Date(2026, 9, 8, 18, 0, 0, 0, time.UTC)
	task, err := core.NewTask(core.NewTaskParams{
		ID: "failed-task", Type: core.TaskConversationSend, IdempotencyKey: "hidden-key",
		Source: "test", ProfileID: "primary", CorrelationID: "correlation-1",
		Payload: json.RawMessage(`{"text":"hidden message"}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if _, err := store.Enqueue(context.Background(), task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	lease, found, err := store.Claim(context.Background(), broker.ClaimParams{
		WorkerID: "worker", TaskType: task.Type, Now: now, LeaseDuration: time.Minute,
	})
	if err != nil || !found {
		t.Fatalf("claim: found=%t err=%v", found, err)
	}
	if err := store.Fail(context.Background(), lease, &core.OperationError{
		Category: core.ErrorUnsupported, Operation: "conversation.send", Message: "old transport unavailable",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	configPath := writeConfig(t, directory, appconfig.Config{
		Database: databaseConfig(databasePath),
	})
	var output bytes.Buffer
	if err := run(context.Background(), []string{configPath}, &output); err != nil {
		t.Fatalf("preflight: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "service=degraded") || !strings.Contains(output.String(), "failed_task=failed-task") ||
		strings.Contains(output.String(), "hidden message") || strings.Contains(output.String(), "hidden-key") {
		t.Fatalf("unexpected output:\n%s", output.String())
	}
}

func TestRunBlocksOnFailedProfileStateApply(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "job-agent.db")
	if err := storesqlite.MigrateUp(databasePath); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Date(2026, 9, 8, 19, 0, 0, 0, time.UTC)
	task, err := core.NewTask(core.NewTaskParams{
		ID: "failed-apply", Type: core.TaskProfileStateApply, IdempotencyKey: "apply-key",
		Source: "test", ProfileID: "primary", CorrelationID: "correlation-1", Payload: json.RawMessage(`{"proposal_id":"proposal-1"}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if _, err := store.Enqueue(context.Background(), task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	lease, found, err := store.Claim(context.Background(), broker.ClaimParams{
		WorkerID: "worker", TaskType: task.Type, Now: now, LeaseDuration: time.Minute,
	})
	if err != nil || !found {
		t.Fatalf("claim: found=%t err=%v", found, err)
	}
	if err := store.Fail(context.Background(), lease, &core.OperationError{
		Category: core.ErrorPermanentFailure, Operation: "profile_state.apply", Message: "invalid field",
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	configPath := writeConfig(t, directory, appconfig.Config{Database: databaseConfig(databasePath)})
	var output bytes.Buffer
	err = run(context.Background(), []string{configPath}, &output)
	if err == nil || !strings.Contains(err.Error(), "preflight blocked") || !strings.Contains(output.String(), "service=blocked") ||
		!strings.Contains(output.String(), "failed profile state apply") {
		t.Fatalf("error=%v output:\n%s", err, output.String())
	}
}

func TestValidateBrowserStateRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"cookies":[{"name":"session","value":"opaque","domain":".hh.ru"}]}`), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := validateBrowserState(path, hh.Name); err == nil || err.Error() != "insecure_permissions" {
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

func databaseConfig(path string) appconfig.DatabaseConfig {
	return appconfig.DatabaseConfig{Driver: "sqlite", Path: path}
}
