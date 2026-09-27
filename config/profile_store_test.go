package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileStoreLoadsDashboardFragments(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	writeConfigTestFile(t, configPath, `{
		"schema_version":1,
		"database":{"driver":"sqlite","path":"data/job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[{"tag":"primary","adapter":"hh-main","enabled":false,"resume":"resume-1"}]
	}`)
	// The default store lives next to the database file; a missing directory is
	// not an error, so the same config works before the first fragment.
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("load without store: %v", err)
	}
	if loaded.Profiles[0].Source != ProfileSourceConfig {
		t.Fatalf("config profile source = %q", loaded.Profiles[0].Source)
	}
	writeConfigTestFile(t, filepath.Join(directory, "data", "profiles", "secondary.json"), `{
		"profiles":[{"tag":"secondary","adapter":"hh-main","enabled":true,
			"resumes":[{"id":"resume-9","title":"Go","primary":true},{"id":"resume-8","primary":false}]}]
	}`)
	loaded, err = Load(configPath)
	if err != nil {
		t.Fatalf("load with store: %v", err)
	}
	if len(loaded.Profiles) != 2 {
		t.Fatalf("profiles = %#v", loaded.Profiles)
	}
	stored := loaded.Profiles[1]
	if stored.Tag != "secondary" || stored.Source != ProfileSourceStore {
		t.Fatalf("stored profile = %#v", stored)
	}
	if stored.Resume != "resume-9" || len(stored.ResumeIDs()) != 2 {
		t.Fatalf("stored resumes = %#v", stored.Resumes)
	}
}

func TestProfileStoreExplicitDirectoryAndDuplicateTag(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	writeConfigTestFile(t, configPath, `{
		"schema_version":1,
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"profile_store":{"dir":"profiles"},
		"adapters":[{"tag":"hh-main","type":"hh"}]
	}`)
	writeConfigTestFile(t, filepath.Join(directory, "profiles", "primary.json"), `{
		"profiles":[{"tag":"primary","adapter":"hh-main","enabled":true}]
	}`)
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Profiles) != 1 || loaded.Profiles[0].Tag != "primary" {
		t.Fatalf("profiles = %#v", loaded.Profiles)
	}
	writeConfigTestFile(t, configPath, `{
		"schema_version":1,
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"profile_store":{"dir":"profiles"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[{"tag":"primary","adapter":"hh-main","enabled":false}]
	}`)
	if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), "duplicate profile tag") {
		t.Fatalf("error = %v", err)
	}
}

func TestProfileStoreDirectoryResolution(t *testing.T) {
	relative := Config{
		Database:     DatabaseConfig{Path: "data/job-agent.db"},
		ProfileStore: ProfileStoreConfig{Dir: "store"},
	}
	if dir := relative.ProfileStoreDirectory("/etc/job-agent"); dir != "/etc/job-agent/store" {
		t.Fatalf("relative dir = %q", dir)
	}
	absolute := Config{
		Database:     DatabaseConfig{Path: "data/job-agent.db"},
		ProfileStore: ProfileStoreConfig{Dir: "/srv/profiles"},
	}
	if dir := absolute.ProfileStoreDirectory("/etc/job-agent"); dir != "/srv/profiles" {
		t.Fatalf("absolute dir = %q", dir)
	}
	fallback := Config{Database: DatabaseConfig{Path: "./data/job-agent.db"}}
	if dir := fallback.ProfileStoreDirectory("/etc/job-agent"); dir != "/etc/job-agent/data/profiles" {
		t.Fatalf("default dir = %q", dir)
	}
}

func TestProfileStoreIsMainFileOnly(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	writeConfigTestFile(t, configPath, `{
		"schema_version":1,
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"include":["extra.json"]
	}`)
	writeConfigTestFile(t, filepath.Join(directory, "extra.json"), `{"profile_store":{"dir":"profiles"}}`)
	if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), "profile_store is only allowed in the main file") {
		t.Fatalf("error = %v", err)
	}
}
