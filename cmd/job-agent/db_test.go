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

	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

func dbTestFixture(t *testing.T) (string, string, string) {
	t.Helper()
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "job-agent.db")
	configPath := filepath.Join(directory, "config.json")
	config := map[string]any{
		"database": map[string]string{"driver": "sqlite", "path": databasePath},
		"adapters": []map[string]string{{"tag": "hh-main", "type": "hh"}},
		"profiles": []map[string]any{},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := storesqlite.MigrateUp(databasePath); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-1", Type: core.TaskVacancySearchPage, IdempotencyKey: "search-1",
		Source: "test", Platform: "hh", ProfileID: "primary", CorrelationID: "correlation-1",
		Payload: json.RawMessage(`{}`),
	}, now)
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if _, err := store.Enqueue(context.Background(), task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	return directory, databasePath, configPath
}

func TestDBBackupAndGuardedRestore(t *testing.T) {
	directory, databasePath, configPath := dbTestFixture(t)
	backupPath := filepath.Join(directory, "backup.db")
	var output bytes.Buffer
	if err := runDBBackup(context.Background(), []string{"--config", configPath, "--output", backupPath}, &output); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if !strings.Contains(output.String(), "BACKUP") || !strings.Contains(output.String(), "schema=") {
		t.Fatalf("backup output = %q", output.String())
	}
	version, err := storesqlite.VerifyDatabase(backupPath)
	if err != nil || version == 0 {
		t.Fatalf("verify backup: version=%d err=%v", version, err)
	}
	if err := runDBBackup(context.Background(), []string{"--config", configPath, "--output", backupPath}, &output); err == nil {
		t.Fatal("expected existing backup destination to fail")
	}
	if err := runDBRestore([]string{"--config", configPath, "--input", backupPath}, &output); err == nil {
		t.Fatal("expected restore without --force to fail")
	}
	if err := runDBRestore([]string{"--config", configPath, "--input", backupPath, "--force"}, &output); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !strings.Contains(output.String(), "RESTORE") {
		t.Fatalf("restore output = %q", output.String())
	}
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	defer store.Close()
	if _, err := store.TaskByID(context.Background(), "task-1"); err != nil {
		t.Fatalf("restored task: %v", err)
	}
	invalid := filepath.Join(directory, "invalid.db")
	if err := os.WriteFile(invalid, []byte("not a database"), 0o600); err != nil {
		t.Fatalf("write invalid source: %v", err)
	}
	if err := runDBRestore([]string{"--config", configPath, "--input", invalid, "--force"}, &output); err == nil {
		t.Fatal("expected invalid restore source to fail")
	}
}
