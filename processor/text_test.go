package processor

import (
	"context"
	"strings"
	"testing"
)

func TestTextProcessorChainTransformsAndRecordsProvenance(t *testing.T) {
	registry := NewRegistry()
	reorder, err := NewReplace([]Replacement{{
		Pattern: `^(.*) \| (.*)$`, With: `$2 — $1`, Regexp: true,
	}})
	if err != nil {
		t.Fatalf("new replace: %v", err)
	}
	wrapper, err := NewTemplate(`{{.Variables.Prefix}}: {{.Text}}`)
	if err != nil {
		t.Fatalf("new template: %v", err)
	}
	if err := registry.Register("reorder", reorder); err != nil {
		t.Fatalf("register reorder: %v", err)
	}
	if err := registry.Register("wrapper", wrapper); err != nil {
		t.Fatalf("register wrapper: %v", err)
	}
	chain, err := NewChain(registry, []string{"reorder", "wrapper"})
	if err != nil {
		t.Fatalf("new chain: %v", err)
	}
	if err := registry.Register("about-backend", chain); err != nil {
		t.Fatalf("register chain: %v", err)
	}

	result, err := registry.Process(context.Background(), "about-backend", TextInput{
		Text: "Надёжные сервисы | Go и PostgreSQL", Variables: map[string]string{"Prefix": "Backend"},
	})
	if err != nil {
		t.Fatalf("process chain: %v", err)
	}
	if result.Text != "Backend: Go и PostgreSQL — Надёжные сервисы" {
		t.Fatalf("result text: %q", result.Text)
	}
	if len(result.Steps) != 3 || result.Steps[0].Tag != "reorder" || result.Steps[1].Tag != "wrapper" || result.Steps[2].Type != "chain" {
		t.Fatalf("steps: %#v", result.Steps)
	}
	for _, step := range result.Steps {
		if !strings.HasPrefix(step.InputDigest, "sha256:") || !strings.HasPrefix(step.OutputDigest, "sha256:") || !step.Changed {
			t.Fatalf("invalid provenance: %#v", step)
		}
	}
}

func TestTextProcessorRegistryRejectsDuplicateMissingAndCycles(t *testing.T) {
	registry := NewRegistry()
	replace, err := NewReplace([]Replacement{{Pattern: "a", With: "b"}})
	if err != nil {
		t.Fatalf("new replace: %v", err)
	}
	if err := registry.Register("replace", replace); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register("replace", replace); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
	if _, err := registry.Process(context.Background(), "missing", TextInput{}); err == nil {
		t.Fatal("expected missing processor to fail")
	}
	cycle, err := NewChain(registry, []string{"cycle"})
	if err != nil {
		t.Fatalf("new cycle: %v", err)
	}
	if err := registry.Register("cycle", cycle); err != nil {
		t.Fatalf("register cycle: %v", err)
	}
	if _, err := registry.Process(context.Background(), "cycle", TextInput{Text: "value"}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error: %v", err)
	}
}

func TestTextTemplateRequiresReferencedVariable(t *testing.T) {
	processor, err := NewTemplate(`{{.Variables.Required}}`)
	if err != nil {
		t.Fatalf("new template: %v", err)
	}
	if _, err := processor.Process(context.Background(), TextInput{}); err == nil {
		t.Fatal("expected missing variable to fail")
	}
}

func TestTextInputDigestIncludesVariablesInStableOrder(t *testing.T) {
	first := textInputDigest(TextInput{Text: "value", Variables: map[string]string{"b": "2", "a": "1"}})
	second := textInputDigest(TextInput{Text: "value", Variables: map[string]string{"a": "1", "b": "2"}})
	changed := textInputDigest(TextInput{Text: "value", Variables: map[string]string{"a": "changed", "b": "2"}})
	if first != second || first == changed {
		t.Fatalf("digests must be stable and include variables: %q %q %q", first, second, changed)
	}
}
