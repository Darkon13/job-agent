package approve

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

func TestRunReleasesPreparedApprovalAndEnqueuesIdempotently(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "job-agent.db")
	if err := storesqlite.MigrateUp(databasePath); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	configPath := writeTestConfig(t, directory, databasePath)
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	vacancy := core.Vacancy{Platform: "hh", ExternalID: "42", URL: "https://hh.ru/vacancy/42", Title: "Go developer", State: core.VacancyStateOpen, ObservedAt: now}
	if _, err := store.UpsertVacancy(context.Background(), vacancy); err != nil {
		t.Fatal(err)
	}
	application, err := core.NewApplication("application-1", core.ApplicationKey{ProfileID: "primary", Vacancy: vacancy.Key()}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateApplication(context.Background(), application); err != nil {
		t.Fatal(err)
	}
	if err := application.Transition(core.ApplicationPreparing, now); err != nil {
		t.Fatal(err)
	}
	if err := application.RecordPreparation("qualified", "rules passed", "resume-1", "Здравствуйте!", now); err != nil {
		t.Fatal(err)
	}
	if err := application.Transition(core.ApplicationWaitingApproval, now); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveApplication(context.Background(), application, core.ApplicationNew); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	args := []string{"-idempotency-key", "approval:primary:hh:42", configPath, "primary", "42"}
	var output bytes.Buffer
	if err := run(context.Background(), args, &output, now.Add(time.Minute)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !strings.Contains(output.String(), "status=ready") || !strings.Contains(output.String(), "created=true") {
		t.Fatalf("output = %q", output.String())
	}

	store, err = storesqlite.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stored, err := store.Application(context.Background(), application.Key)
	if err != nil || stored.Status != core.ApplicationReady {
		t.Fatalf("application=%#v err=%v", stored, err)
	}
	task, err := store.TaskByIdempotencyKey(context.Background(), "approval:primary:hh:42")
	if err != nil || task.Type != core.TaskApplicationSubmit || task.Status != core.TaskNew {
		t.Fatalf("task=%#v err=%v", task, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), args, &output, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("repeat approve: %v", err)
	}
	if !strings.Contains(output.String(), "created=false") {
		t.Fatalf("repeat output = %q", output.String())
	}
}

func writeTestConfig(t *testing.T, directory, databasePath string) string {
	t.Helper()
	cfg := appconfig.Config{
		Database: appconfig.DatabaseConfig{Driver: "sqlite", Path: databasePath},
		Adapters: []appconfig.AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []appconfig.Profile{{
			Tag: "primary", Adapter: "hh-main", Resume: "resume-1", Enabled: true,
			Applications: appconfig.ApplicationPolicy{Mode: appconfig.ApplicationModeSubmit, DailyLimit: 1, Message: "Здравствуйте!"},
		}},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
