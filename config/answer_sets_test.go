package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Darkon13/job-agent/core"
)

func TestLoadResolvesAnswerSets(t *testing.T) {
	directory := t.TempDir()
	answerDirectory := filepath.Join(directory, "answers")
	if err := os.Mkdir(answerDirectory, 0o700); err != nil {
		t.Fatalf("create answers directory: %v", err)
	}
	block := `{"tag":"hh-vacancy","name":"HH vacancy","kind":"vacancy","platform":"hh","answers":[{"question":"Опыт?","text":"5 лет"}]}`
	if err := os.WriteFile(filepath.Join(answerDirectory, "hh-vacancy.json"), []byte(block), 0o600); err != nil {
		t.Fatalf("write answer block: %v", err)
	}
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"database":{"driver":"sqlite","path":"job-agent.db"},
		"adapters":[{"tag":"hh-main","type":"hh"}],
		"profiles":[],
		"answer_sets":["answers/hh-vacancy.json"]
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	blocks := loaded.ResolvedAnswerBlocks()
	if len(blocks) != 1 || blocks[0].Kind != core.AnswerBlockVacancy || blocks[0].Tag != "hh-vacancy" {
		t.Fatalf("blocks = %#v", blocks)
	}
	if _, err := Load(filepath.Join(directory, "missing.json")); err == nil {
		t.Fatal("expected missing config error")
	}
}
