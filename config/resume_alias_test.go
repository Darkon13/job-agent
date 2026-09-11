package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Darkon13/job-agent/core"
)

func TestLoadResolvesResumeAliases(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	writeConfigTestFile(t, configPath, `{
		"schema_version":1,
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[
			{"tag":"primary","adapter":"hh-main","resume":"backend","enabled":false,
			 "resume_aliases":{"backend":"resume-1","front":"resume-2"}}
		],
		"jobs":[
			{"tag":"publish-front","enabled":true,"concurrency":"forbid",
			 "triggers":[{"type":"cron","expression":"0 10 * * *","timezone":"Europe/Moscow","misfire":"run_once"}],
			 "action":{"type":"resume.publish","profile":"primary","resume":"front"}}
		]
	}`)
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Profiles[0].Resume != "resume-1" {
		t.Fatalf("profile resume = %q", loaded.Profiles[0].Resume)
	}
	if loaded.Jobs[0].Action.Resume != "resume-2" {
		t.Fatalf("job resume = %q", loaded.Jobs[0].Action.Resume)
	}
	targets := loaded.ResumeTargets()[core.ProfileID("primary")]
	if len(targets) != 3 || !targets[0].Primary || targets[0].ID != "resume-1" {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[1].Alias != "backend" || targets[2].Alias != "front" {
		t.Fatalf("aliases = %#v", targets)
	}
}

func TestLoadKeepsRawResumeIDsWithoutAliases(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	writeConfigTestFile(t, configPath, `{
		"schema_version":1,
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[{"tag":"primary","adapter":"hh-main","enabled":false,"resume":"resume-1"}],
		"jobs":[
			{"tag":"publish-raw","enabled":true,"concurrency":"forbid",
			 "triggers":[{"type":"cron","expression":"0 10 * * *","timezone":"Europe/Moscow","misfire":"run_once"}],
			 "action":{"type":"resume.publish","profile":"primary","resume":"resume-7"}}
		]
	}`)
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Jobs[0].Action.Resume != "resume-7" {
		t.Fatalf("job resume = %q", loaded.Jobs[0].Action.Resume)
	}
}

func TestLoadRejectsEmptyResumeAlias(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	writeConfigTestFile(t, configPath, `{
		"schema_version":1,
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[{"tag":"primary","adapter":"hh-main","enabled":false,"resume_aliases":{"":"resume-1"}}]
	}`)
	if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), "resume_aliases") {
		t.Fatalf("error = %v", err)
	}
}
