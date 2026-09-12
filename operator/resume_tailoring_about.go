package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Darkon13/job-agent/core"
)

const resumeTailoringAboutInstruction = `Rewrite the resume "About" section for one application.
Use only the current about text, resume facts and vacancy context supplied below. Do not invent experience, skills, employers, education, numbers, contacts or availability.
Keep the first-person voice, stay concise and prefer the themes that matter for this vacancy.
Return one short paragraph. Declared placeholders like {name} may be used verbatim and must not be reworded.`

// ResumeTailoringAboutRequest is the provider-neutral model input for the
// "About" rewrite. The context is already anonymized: personal values are
// replaced with placeholders before the request is built.
type ResumeTailoringAboutRequest struct {
	Instruction    string         `json:"instruction"`
	PromptVersion  string         `json:"prompt_version"`
	VacancyTitle   string         `json:"vacancy_title,omitempty"`
	VacancySkills  []string       `json:"vacancy_skills,omitempty"`
	CurrentAbout   string         `json:"current_about,omitempty"`
	Facts          map[string]any `json:"facts,omitempty"`
	MaximumRunes   int            `json:"maximum_runes"`
	ProfileContext string         `json:"profile_context,omitempty"`
}

type ResumeTailoringAboutResponse struct {
	About      string `json:"about"`
	Model      string `json:"model,omitempty"`
	ResponseID string `json:"response_id,omitempty"`
}

type ResumeTailoringAboutModel interface {
	RewriteAbout(context.Context, ResumeTailoringAboutRequest) (ResumeTailoringAboutResponse, error)
}

type ModelResumeTailoringAboutConfig struct {
	Tag           string
	PromptVersion string
	Instruction   string
	MaximumRunes  int
	Timeout       time.Duration
	Facts         *ApplicationResumeContext
	Model         ResumeTailoringAboutModel
}

// ModelResumeTailoringAboutProcessor rewrites the declared "About" path with a
// model and falls back to keeping the current text on any model failure or
// invalid output, so skills tailoring can still proceed.
type ModelResumeTailoringAboutProcessor struct {
	tag           string
	promptVersion string
	instruction   string
	maximumRunes  int
	timeout       time.Duration
	facts         *ApplicationResumeContext
	model         ResumeTailoringAboutModel
}

func NewModelResumeTailoringAboutProcessor(config ModelResumeTailoringAboutConfig) (*ModelResumeTailoringAboutProcessor, error) {
	processor := &ModelResumeTailoringAboutProcessor{
		tag: strings.TrimSpace(config.Tag), promptVersion: strings.TrimSpace(config.PromptVersion),
		instruction: strings.TrimSpace(config.Instruction), maximumRunes: config.MaximumRunes,
		timeout: config.Timeout, facts: config.Facts, model: config.Model,
	}
	if processor.tag == "" || processor.promptVersion == "" || processor.instruction == "" {
		return nil, errors.New("model resume tailoring about requires tag, prompt version and instruction")
	}
	if processor.maximumRunes < 1 {
		return nil, errors.New("model resume tailoring about requires a positive rune limit")
	}
	if processor.timeout <= 0 {
		return nil, errors.New("model resume tailoring about requires a positive timeout")
	}
	if processor.model == nil || processor.facts == nil || len(processor.facts.Facts) == 0 {
		return nil, errors.New("model resume tailoring about requires a model and resume facts")
	}
	return processor, nil
}

func (processor *ModelResumeTailoringAboutProcessor) Plan(ctx context.Context, input ResumeTailoringInput) (ResumeTailoringPlan, error) {
	if processor == nil {
		return ResumeTailoringPlan{}, errors.New("model resume tailoring about processor is nil")
	}
	if err := input.Validate(); err != nil {
		return ResumeTailoringPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return ResumeTailoringPlan{}, err
	}
	path := ResumeAboutPath(input.ResumeID)
	if !slicesContains(input.AllowedPaths, path) {
		return ResumeTailoringPlan{}, fmt.Errorf("resume tailoring about path %q is not allowed", path)
	}
	current, err := observedResumeAbout(input.CurrentState, path)
	if err != nil {
		return ResumeTailoringPlan{}, err
	}
	plan, err := processor.planWithModel(ctx, input, path, current)
	if err == nil {
		return plan, nil
	}
	if ctx.Err() != nil {
		return ResumeTailoringPlan{}, ctx.Err()
	}
	// Keep the current text; skills tailoring continues independently.
	return processor.emptyPlan(input, path, current), nil
}

func (processor *ModelResumeTailoringAboutProcessor) planWithModel(ctx context.Context, input ResumeTailoringInput, path, current string) (ResumeTailoringPlan, error) {
	anonymousFacts, anonymousAbout, placeholders, err := anonymizeResumeTailoringAbout(processor.facts.Facts, current)
	if err != nil {
		return ResumeTailoringPlan{}, err
	}
	modelCtx, cancel := context.WithTimeout(ctx, processor.timeout)
	defer cancel()
	response, err := processor.model.RewriteAbout(modelCtx, ResumeTailoringAboutRequest{
		Instruction:   resumeTailoringAboutInstruction + "\n\n" + processor.instruction,
		PromptVersion: processor.promptVersion,
		VacancyTitle:  input.Vacancy.Title,
		VacancySkills: vacancyAttributeStrings(input.Vacancy, "key_skills"),
		CurrentAbout:  anonymousAbout,
		Facts:         anonymousFacts,
		MaximumRunes:  processor.maximumRunes,
	})
	if err != nil {
		return ResumeTailoringPlan{}, err
	}
	text := strings.TrimSpace(response.About)
	if err := validateResumeTailoringAboutText(text, anonymousAbout, anonymousFacts, input.Vacancy.Title, vacancyAttributeStrings(input.Vacancy, "key_skills"), processor.maximumRunes, placeholders); err != nil {
		return ResumeTailoringPlan{}, err
	}
	substituted, err := substituteApplicationPlaceholders(text, placeholders)
	if err != nil {
		return ResumeTailoringPlan{}, err
	}
	substituted = strings.TrimSpace(substituted)
	if substituted == strings.TrimSpace(current) {
		return processor.emptyPlan(input, path, current), nil
	}
	encoded, err := json.Marshal([]string{substituted})
	if err != nil {
		return ResumeTailoringPlan{}, fmt.Errorf("encode tailored resume about: %w", err)
	}
	plan := ResumeTailoringPlan{
		ProcessorTag: processor.tag, ProcessorVersion: processor.promptVersion,
		InputDigest: resumeTailoringAboutInputDigest(input, path, current, anonymousFacts),
		Overrides:   []core.ProfileStateValueOverride{{Path: path, Value: encoded}},
	}
	if err := plan.Validate(input); err != nil {
		return ResumeTailoringPlan{}, err
	}
	return plan, nil
}

func (processor *ModelResumeTailoringAboutProcessor) emptyPlan(input ResumeTailoringInput, path, current string) ResumeTailoringPlan {
	anonymousFacts, _, _, _ := anonymizeResumeTailoringAbout(processor.facts.Facts, current)
	return ResumeTailoringPlan{
		ProcessorTag: processor.tag, ProcessorVersion: processor.promptVersion,
		InputDigest: resumeTailoringAboutInputDigest(input, path, current, anonymousFacts),
	}
}

func anonymizeResumeTailoringAbout(facts map[string]any, about string) (map[string]any, string, map[string]string, error) {
	declared, err := declaredApplicationPlaceholders(facts[applicationModelPlaceholdersFactKey])
	if err != nil {
		return nil, "", nil, err
	}
	replacements := applicationPlaceholderReplacements(declared)
	anonymousFacts, err := anonymizedFacts(facts, replacements)
	if err != nil {
		return nil, "", nil, err
	}
	anonymousAbout, _ := replaceApplicationPlaceholderValues(about, replacements).(string)
	return anonymousFacts, anonymousAbout, declared, nil
}

func validateResumeTailoringAboutText(text, currentAbout string, facts map[string]any, vacancyTitle string, vacancySkills []string, maximumRunes int, placeholders map[string]string) error {
	invalid := func(message string) error {
		return fmt.Errorf("model resume tailoring about returned invalid output: %s", message)
	}
	if text == "" {
		return invalid("empty about text")
	}
	if utf8.RuneCountInString(text) > maximumRunes {
		return invalid("about text is longer than the configured limit")
	}
	if strings.Contains(text, "{{") || strings.Contains(text, "}}") {
		return invalid("unresolved template placeholder")
	}
	if undeclared := undeclaredApplicationPlaceholders(text, placeholders); len(undeclared) != 0 {
		return invalid("undeclared placeholder " + undeclared[0])
	}
	if strings.Contains(text, "```") {
		return invalid("fenced service response")
	}
	trimmed := strings.TrimSpace(text)
	if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(trimmed)) {
		return invalid("structured service response")
	}
	for _, value := range text {
		if unicode.IsControl(value) && value != '\n' && value != '\r' && value != '\t' {
			return invalid("unsupported control character")
		}
	}
	grounding, err := json.Marshal(struct {
		CurrentAbout  string         `json:"current_about,omitempty"`
		Facts         map[string]any `json:"facts,omitempty"`
		VacancyTitle  string         `json:"vacancy_title,omitempty"`
		VacancySkills []string       `json:"vacancy_skills,omitempty"`
	}{CurrentAbout: currentAbout, Facts: facts, VacancyTitle: vacancyTitle, VacancySkills: vacancySkills})
	if err != nil {
		return invalid("grounding context could not be encoded")
	}
	groundingText := strings.ToLower(string(grounding))
	for _, check := range []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{name: "email", pattern: applicationModelEmailPattern},
		{name: "URL", pattern: applicationModelURLPattern},
	} {
		allowed := modelTokenSet(check.pattern, groundingText)
		for token := range modelTokenSet(check.pattern, strings.ToLower(text)) {
			if _, exists := allowed[token]; !exists {
				return invalid("ungrounded " + check.name)
			}
		}
	}
	textWithoutContacts := applicationModelURLPattern.ReplaceAllString(text, " ")
	textWithoutContacts = applicationModelEmailPattern.ReplaceAllString(textWithoutContacts, " ")
	groundingWithoutContacts := applicationModelURLPattern.ReplaceAllString(groundingText, " ")
	groundingWithoutContacts = applicationModelEmailPattern.ReplaceAllString(groundingWithoutContacts, " ")
	allowedNumbers := modelTokenSet(applicationModelNumberPattern, groundingWithoutContacts)
	for number := range modelTokenSet(applicationModelNumberPattern, strings.ToLower(textWithoutContacts)) {
		if _, exists := allowedNumbers[number]; !exists {
			return invalid("ungrounded number")
		}
	}
	return nil
}

func ResumeAboutPath(resumeID string) string {
	return "/resumes/" + escapeResumeTailoringPointer(strings.TrimSpace(resumeID)) + "/web/skills"
}

func observedResumeAbout(observation core.ProfileStateObservation, path string) (string, error) {
	raw, exists, err := observation.ValueAt(path)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("observed resume state does not contain restorable about path %q", path)
	}
	if string(raw) == "null" {
		return "", nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return "", fmt.Errorf("observed resume about at %q must be a string array: %w", path, err)
	}
	if len(values) > 1 {
		return "", fmt.Errorf("observed resume about at %q contains more than one value", path)
	}
	if len(values) == 0 {
		return "", nil
	}
	return values[0], nil
}

func resumeTailoringAboutInputDigest(input ResumeTailoringInput, path, currentAbout string, facts map[string]any) string {
	encoded, _ := json.Marshal(struct {
		ApplicationID core.ApplicationID `json:"application_id"`
		ProfileID     core.ProfileID     `json:"profile_id"`
		ResumeID      string             `json:"resume_id"`
		Path          string             `json:"path"`
		CurrentAbout  string             `json:"current_about"`
		Facts         map[string]any     `json:"facts"`
	}{
		ApplicationID: input.Application.ID, ProfileID: input.Application.Key.ProfileID,
		ResumeID: input.ResumeID, Path: path, CurrentAbout: currentAbout, Facts: facts,
	})
	return applicationBytesDigest(encoded)
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
