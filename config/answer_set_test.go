package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAnswerBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answers.json")
	data := []byte(`{
  "tag": "reviewed-example",
  "name": "Reviewed example",
  "kind": "qualification",
  "platform": "mock",
  "answers": [
    {"question": "Synthetic question", "selected_options": ["Reviewed option"]}
  ]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	block, err := LoadAnswerBlock(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if block.Tag != "reviewed-example" || block.Name != "Reviewed example" || len(block.Answers) != 1 {
		t.Fatalf("unexpected answer block: %#v", block)
	}
}

func TestLoadAnswerBlockRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answers.json")
	data := []byte(`{
  "tag": "reviewed-example",
  "name": "Reviewed example",
  "kind": "qualification",
  "platform": "mock",
  "answers": [{"question": "Synthetic question", "selected_options": ["Reviewed option"], "typo": true}]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := LoadAnswerBlock(path); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
}
