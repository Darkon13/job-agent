package processor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/template"
	"unicode/utf8"
)

const maximumTextRunes = 100_000

type TextInput struct {
	Text      string            `json:"text"`
	Variables map[string]string `json:"variables,omitempty"`
}

type TextStep struct {
	Tag          string   `json:"tag"`
	Type         string   `json:"type"`
	InputDigest  string   `json:"input_digest"`
	OutputDigest string   `json:"output_digest"`
	Changed      bool     `json:"changed"`
	Warnings     []string `json:"warnings,omitempty"`
}

type TextOutput struct {
	Text     string     `json:"text"`
	Steps    []TextStep `json:"steps,omitempty"`
	Warnings []string   `json:"warnings,omitempty"`
}

type TextProcessor interface {
	Type() string
	Process(context.Context, TextInput) (TextOutput, error)
}

type Registry struct {
	mu         sync.RWMutex
	processors map[string]TextProcessor
}

func NewRegistry() *Registry {
	return &Registry{processors: make(map[string]TextProcessor)}
}

func (registry *Registry) Register(tag string, instance TextProcessor) error {
	tag = strings.TrimSpace(tag)
	if registry == nil || tag == "" || instance == nil || strings.TrimSpace(instance.Type()) == "" {
		return errors.New("processor registration requires registry, tag, type and implementation")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.processors[tag]; exists {
		return fmt.Errorf("processor %q is already registered", tag)
	}
	registry.processors[tag] = instance
	return nil
}

func (registry *Registry) Process(ctx context.Context, tag string, input TextInput) (TextOutput, error) {
	if registry == nil {
		return TextOutput{}, errors.New("processor registry is nil")
	}
	if err := validateText(input.Text); err != nil {
		return TextOutput{}, fmt.Errorf("processor input: %w", err)
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return TextOutput{}, errors.New("processor tag is required")
	}
	stack, _ := ctx.Value(processorStackKey{}).([]string)
	for _, ancestor := range stack {
		if ancestor == tag {
			return TextOutput{}, fmt.Errorf("processor chain cycle: %s -> %s", strings.Join(stack, " -> "), tag)
		}
	}
	registry.mu.RLock()
	instance, exists := registry.processors[tag]
	registry.mu.RUnlock()
	if !exists {
		return TextOutput{}, fmt.Errorf("processor %q is not registered", tag)
	}
	if err := ctx.Err(); err != nil {
		return TextOutput{}, err
	}
	childCtx := context.WithValue(ctx, processorStackKey{}, append(append([]string(nil), stack...), tag))
	output, err := instance.Process(childCtx, cloneInput(input))
	if err != nil {
		return TextOutput{}, fmt.Errorf("processor %q: %w", tag, err)
	}
	if err := validateText(output.Text); err != nil {
		return TextOutput{}, fmt.Errorf("processor %q output: %w", tag, err)
	}
	warnings := append([]string(nil), output.Warnings...)
	output.Steps = append(output.Steps, TextStep{
		Tag: tag, Type: instance.Type(), InputDigest: textInputDigest(input),
		OutputDigest: textDigest(output.Text), Changed: output.Text != input.Text,
		Warnings: warnings,
	})
	output.Warnings = warnings
	return output, nil
}

type processorStackKey struct{}

type replacement struct {
	pattern string
	with    string
	regex   *regexp.Regexp
}

type Replacement struct {
	Pattern string `json:"pattern"`
	With    string `json:"with"`
	Regexp  bool   `json:"regexp,omitempty"`
}

type Replace struct{ replacements []replacement }

func NewReplace(configured []Replacement) (*Replace, error) {
	if len(configured) == 0 {
		return nil, errors.New("text.replace requires at least one replacement")
	}
	result := &Replace{replacements: make([]replacement, 0, len(configured))}
	for index, item := range configured {
		if item.Pattern == "" {
			return nil, fmt.Errorf("replacement %d requires pattern", index)
		}
		compiled := replacement{pattern: item.Pattern, with: item.With}
		if item.Regexp {
			value, err := regexp.Compile(item.Pattern)
			if err != nil {
				return nil, fmt.Errorf("replacement %d regexp: %w", index, err)
			}
			compiled.regex = value
		}
		result.replacements = append(result.replacements, compiled)
	}
	return result, nil
}

func (*Replace) Type() string { return "text.replace" }

func (processor *Replace) Process(ctx context.Context, input TextInput) (TextOutput, error) {
	if processor == nil {
		return TextOutput{}, errors.New("text.replace is nil")
	}
	value := input.Text
	for _, item := range processor.replacements {
		if err := ctx.Err(); err != nil {
			return TextOutput{}, err
		}
		if item.regex != nil {
			value = item.regex.ReplaceAllString(value, item.with)
		} else {
			value = strings.ReplaceAll(value, item.pattern, item.with)
		}
	}
	return TextOutput{Text: value}, nil
}

type Template struct{ compiled *template.Template }

func NewTemplate(source string) (*Template, error) {
	if strings.TrimSpace(source) == "" {
		return nil, errors.New("text.template requires source")
	}
	compiled, err := template.New("text").Option("missingkey=error").Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse text.template: %w", err)
	}
	return &Template{compiled: compiled}, nil
}

func (*Template) Type() string { return "text.template" }

func (processor *Template) Process(ctx context.Context, input TextInput) (TextOutput, error) {
	if processor == nil || processor.compiled == nil {
		return TextOutput{}, errors.New("text.template is nil")
	}
	if err := ctx.Err(); err != nil {
		return TextOutput{}, err
	}
	var output bytes.Buffer
	if err := processor.compiled.Execute(&output, struct {
		Text      string
		Variables map[string]string
	}{Text: input.Text, Variables: input.Variables}); err != nil {
		return TextOutput{}, fmt.Errorf("execute text.template: %w", err)
	}
	return TextOutput{Text: output.String()}, nil
}

type Chain struct {
	registry *Registry
	steps    []string
}

func NewChain(registry *Registry, steps []string) (*Chain, error) {
	if registry == nil || len(steps) == 0 {
		return nil, errors.New("processor chain requires registry and steps")
	}
	seen := make(map[string]struct{}, len(steps))
	result := make([]string, 0, len(steps))
	for index, step := range steps {
		step = strings.TrimSpace(step)
		if step == "" {
			return nil, fmt.Errorf("processor chain step %d requires tag", index)
		}
		if _, exists := seen[step]; exists {
			return nil, fmt.Errorf("processor chain contains duplicate step %q", step)
		}
		seen[step] = struct{}{}
		result = append(result, step)
	}
	return &Chain{registry: registry, steps: result}, nil
}

func (*Chain) Type() string { return "chain" }

func (chain *Chain) Process(ctx context.Context, input TextInput) (TextOutput, error) {
	if chain == nil || chain.registry == nil {
		return TextOutput{}, errors.New("processor chain is nil")
	}
	result := TextOutput{Text: input.Text}
	for _, tag := range chain.steps {
		step, err := chain.registry.Process(ctx, tag, TextInput{Text: result.Text, Variables: input.Variables})
		if err != nil {
			return TextOutput{}, err
		}
		result.Text = step.Text
		result.Steps = append(result.Steps, step.Steps...)
		result.Warnings = append(result.Warnings, step.Warnings...)
	}
	return result, nil
}

func validateText(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("text must be valid UTF-8")
	}
	if utf8.RuneCountInString(value) > maximumTextRunes {
		return fmt.Errorf("text must contain at most %d characters", maximumTextRunes)
	}
	return nil
}

func textDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func textInputDigest(input TextInput) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(input.Text))
	keys := make([]string, 0, len(input.Variables))
	for key := range input.Variables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(key))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(input.Variables[key]))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func cloneInput(input TextInput) TextInput {
	result := TextInput{Text: input.Text}
	if input.Variables != nil {
		result.Variables = make(map[string]string, len(input.Variables))
		for key, value := range input.Variables {
			result.Variables[key] = value
		}
	}
	return result
}
