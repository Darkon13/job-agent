package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadMergesIncludesAndResolvesTheirFiles(t *testing.T) {
	directory := t.TempDir()
	writeConfigTestFile(t, filepath.Join(directory, "includes", "resumes", "backend.json"), `{
		"resume_id":"resume-1",
		"facts":{"headline":"Backend developer"}
	}`)
	writeConfigTestFile(t, filepath.Join(directory, "includes", "answers", "hh.json"), `{
		"tag":"hh-vacancy","name":"HH vacancy","kind":"vacancy","platform":"hh",
		"answers":[{"question":"Опыт?","text":"5 лет"}]
	}`)
	writeConfigTestFile(t, filepath.Join(directory, "includes", "a.json"), `{
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[{"tag":"primary","adapter":"hh-main","resume":"resume-1","enabled":true,"resume_facts_file":"resumes/backend.json"}],
		"answer_sets":["answers/hh.json"]
	}`)
	writeConfigTestFile(t, filepath.Join(directory, "includes", "b.json"), `{
		"searches":[{"tag":"golang","adapter":"hh-main","profiles":["primary"],"query":{"source":"global","text":"Golang"}}]
	}`)
	configPath := filepath.Join(directory, "config.json")
	writeConfigTestFile(t, configPath, `{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"other","type":"hh"}],
		"include":["includes/*.json"]
	}`)

	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Adapters) != 2 || loaded.Adapters[0].Tag != "other" {
		t.Fatalf("adapters = %#v", loaded.Adapters)
	}
	if len(loaded.Profiles) != 1 || len(loaded.Searches) != 1 {
		t.Fatalf("profiles=%d searches=%d", len(loaded.Profiles), len(loaded.Searches))
	}
	blocks := loaded.ResolvedAnswerBlocks()
	if len(blocks) != 1 || blocks[0].Tag != "hh-vacancy" {
		t.Fatalf("blocks = %#v", blocks)
	}
	facts, exists := loaded.Profiles[0].ResolvedResumeFacts()
	if !exists || facts.ResumeID != "resume-1" {
		t.Fatalf("facts = %#v exists=%v", facts, exists)
	}
}

func TestLoadRejectsDuplicateTagsAcrossIncludes(t *testing.T) {
	directory := t.TempDir()
	writeConfigTestFile(t, filepath.Join(directory, "extra.json"), `{"adapters":[{"tag":"hh-main","type":"hh"}]}`)
	configPath := filepath.Join(directory, "config.json")
	writeConfigTestFile(t, configPath, `{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"include":["extra.json"]
	}`)
	_, err := Load(configPath)
	if err == nil || !strings.Contains(err.Error(), "duplicate adapter tag") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsMissingIncludeGlobAndCycles(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	writeConfigTestFile(t, configPath, `{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"include":["missing/*.json"]
	}`)
	if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), "matched no files") {
		t.Fatalf("missing glob error = %v", err)
	}

	writeConfigTestFile(t, filepath.Join(directory, "a.json"), `{"include":["b.json"]}`)
	writeConfigTestFile(t, filepath.Join(directory, "b.json"), `{"include":["a.json"]}`)
	writeConfigTestFile(t, configPath, `{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"include":["a.json"]
	}`)
	if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), "include cycle") {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestLoadRejectsScalarSectionsInIncludes(t *testing.T) {
	directory := t.TempDir()
	writeConfigTestFile(t, filepath.Join(directory, "extra.json"), `{"database":{"driver":"sqlite","path":"other.db"}}`)
	configPath := filepath.Join(directory, "config.json")
	writeConfigTestFile(t, configPath, `{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"include":["extra.json"]
	}`)
	_, err := Load(configPath)
	if err == nil || !strings.Contains(err.Error(), "only allowed in the main file") {
		t.Fatalf("error = %v", err)
	}
}
