package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMigratesConfiguredDatabase(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "job-agent.db")
	configPath := filepath.Join(directory, "config.json")
	config := fmt.Sprintf(`{"database":{"driver":"sqlite","path":%q}}`, databasePath)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var output bytes.Buffer
	if err := run([]string{"-config", configPath, "up"}, &output); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	output.Reset()
	if err := run([]string{"-config", configPath, "version"}, &output); err != nil {
		t.Fatalf("migration version: %v", err)
	}
	if !strings.Contains(output.String(), "version: 3, dirty: false") {
		t.Fatalf("unexpected version output: %q", output.String())
	}
}

func TestRunRequiresBoundedDownMigration(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	config := fmt.Sprintf(`{"database":{"driver":"sqlite","path":%q}}`, filepath.Join(directory, "job-agent.db"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := run([]string{"-config", configPath, "down"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unbounded down migration to be rejected")
	}
}
