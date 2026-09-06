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

	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

func TestRunEnqueuesResumeTouchIdempotently(t *testing.T) {
	directory := t.TempDir()
	configPath, databasePath := triggerConfig(t, directory)
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	var output bytes.Buffer
	args := []string{"-idempotency-key", "manual-touch-1", configPath, "touch-primary"}
	if err := run(context.Background(), args, &output, now); err != nil {
		t.Fatalf("first trigger: %v", err)
	}
	if err := run(context.Background(), args, &output, now.Add(time.Minute)); err != nil {
		t.Fatalf("second trigger: %v", err)
	}
	if !strings.Contains(output.String(), "created=true") || !strings.Contains(output.String(), "created=false") {
		t.Fatalf("unexpected output:\n%s", output.String())
	}
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	task, err := store.TaskByIdempotencyKey(context.Background(), "manual-touch-1")
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	var payload core.ResumeTouchPayload
	if task.Type != core.TaskResumeTouch || json.Unmarshal(task.Payload, &payload) != nil || payload.ResumeID != "resume-1" {
		t.Fatalf("unexpected task: %#v payload=%#v", task, payload)
	}
}

func TestRunRequiresExplicitIdempotencyKey(t *testing.T) {
	var output bytes.Buffer
	err := run(context.Background(), []string{"config.json", "touch-primary"}, &output, time.Now())
	if err == nil || !strings.Contains(err.Error(), "idempotency-key") {
		t.Fatalf("error = %v", err)
	}
}

func triggerConfig(t *testing.T, directory string) (string, string) {
	t.Helper()
	databasePath := filepath.Join(directory, "job-agent.db")
	if err := storesqlite.MigrateUp(databasePath); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	cfg := appconfig.Config{
		Database: appconfig.DatabaseConfig{Driver: "sqlite", Path: databasePath},
		Adapters: []appconfig.AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []appconfig.Profile{{Tag: "primary", Adapter: "hh-main", Resume: "resume-1", Enabled: true}},
		Jobs: []appconfig.Job{{
			Tag: "touch-primary", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
			Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "0 * * * *", Timezone: "UTC", Misfire: "run_once"}},
			Action:   appconfig.JobAction{Type: appconfig.JobActionResumeTouch, Profile: "primary"},
		}},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath, databasePath
}
