package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Darkon13/job-agent/questionbank"
)

func TestRunImportsMarkdownBanksWithProvenance(t *testing.T) {
	source := t.TempDir()
	output := t.TempDir()
	directory := filepath.Join(source, "docker")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	markdown := `## Docker — базовый уровень

#### Q1. Что запустить?
- [ ] docker build
- [x] docker run
`
	if err := os.WriteFile(filepath.Join(directory, "basic.md"), []byte(markdown), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	err := run([]string{
		"-source", source, "-out", output, "-revision", "deadbeef",
	}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "1 banks: 1 questions, 1 complete") {
		t.Fatalf("unexpected report: %s", stdout.String())
	}
	bank, err := questionbank.ReadFile(filepath.Join(output, "docker", "basic.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bank.Platform != "study" || bank.TargetPlatform != "hh" {
		t.Fatalf("unsafe platform scope: %#v", bank)
	}
	if bank.Source.Revision != "deadbeef" || bank.Qualification.FamilyName != "Docker" {
		t.Fatalf("missing provenance or title metadata: %#v", bank)
	}
}
