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

const resumeTailoringExperienceInstruction = `Rewrite the work experience descriptions of one resume for a specific application.
Use only the current entry text, the supplied resume context and the vacancy context. Do not invent employers, positions, dates, technologies, numbers, contacts or achievements.
Keep each description in its original language and voice, keep every fact, and change the emphasis only towards what matters for this vacancy.
Return every entry you were given with its id unchanged.`

// ResumeTailoringExperienceEntry is one work experience block as the model
// sees it: a stable id, the position and employer for context, and the
// description it may rewrite.
type ResumeTailoringExperienceEntry struct {
	ID          string `json:"id"`
	Position    string `json:"position,omitempty"`
	Company     string `json:"company,omitempty"`
	Description string `json:"description"`
}

type ResumeTailoringExperienceRequest struct {
	Instruction   string                           `json:"instruction"`
	PromptVersion string                           `json:"prompt_version"`
	VacancyTitle  string                           `json:"vacancy_title,omitempty"`
	VacancySkills []string                         `json:"vacancy_skills,omitempty"`
	ResumeContext map[string]any                   `json:"resume_context,omitempty"`
	Entries       []ResumeTailoringExperienceEntry `json:"entries"`
	MaximumRunes  int                              `json:"maximum_runes"`
}

type ResumeTailoringExperienceResponse struct {
	Entries    []ResumeTailoringExperienceEntry `json:"entries"`
	Model      string                           `json:"model,omitempty"`
	ResponseID string                           `json:"response_id,omitempty"`
}

type ResumeTailoringExperienceModel interface {
	RewriteExperience(context.Context, ResumeTailoringExperienceRequest) (ResumeTailoringExperienceResponse, error)
}

type ModelResumeTailoringExperienceConfig struct {
	Tag           string
	PromptVersion string
	Instruction   string
	MaximumRunes  int
	Timeout       time.Duration
	Model         ResumeTailoringExperienceModel
}

// ModelResumeTailoringExperienceProcessor rewrites work experience
// descriptions with a model and keeps the current text on any model failure or
// invalid output, so the application still proceeds with the existing resume.
type ModelResumeTailoringExperienceProcessor struct {
	tag           string
	promptVersion string
	instruction   string
	maximumRunes  int
	timeout       time.Duration
	model         ResumeTailoringExperienceModel
}

const (
	defaultResumeTailoringExperienceMaximumRunes = 1200
	maximumResumeTailoringExperienceRunes        = 4000
)

func NewModelResumeTailoringExperienceProcessor(config ModelResumeTailoringExperienceConfig) (*ModelResumeTailoringExperienceProcessor, error) {
	tag := strings.TrimSpace(config.Tag)
	version := strings.TrimSpace(config.PromptVersion)
	instruction := strings.TrimSpace(config.Instruction)
	if tag == "" || version == "" || instruction == "" || config.Model == nil || config.Timeout <= 0 {
		return nil, errors.New("model experience tailoring requires tag, prompt version, instruction, timeout and model")
	}
	maximum := config.MaximumRunes
	if maximum == 0 {
		maximum = defaultResumeTailoringExperienceMaximumRunes
	}
	if maximum < 1 || maximum > maximumResumeTailoringExperienceRunes {
		return nil, fmt.Errorf("model experience tailoring maximum runes must be between 1 and %d", maximumResumeTailoringExperienceRunes)
	}
	return &ModelResumeTailoringExperienceProcessor{
		tag: tag, promptVersion: version, instruction: instruction,
		maximumRunes: maximum, timeout: config.Timeout, model: config.Model,
	}, nil
}

// ResumeExperiencePath points at the resume work experience array in the HH
// browser profile document.
func ResumeExperiencePath(resumeID string) string {
	return "/resumes/" + escapeResumeTailoringPointer(strings.TrimSpace(resumeID)) + "/web_profile/experience"
}

// ResumeTailoringExperienceReadPaths returns the read paths the experience
// processor needs in the tailoring allowlist.
func ResumeTailoringExperienceReadPaths(resumeID string) []string {
	escaped := escapeResumeTailoringPointer(strings.TrimSpace(resumeID))
	return []string{
		"/resumes/" + escaped + "/web/title",
		"/resumes/" + escaped + "/web/keySkills",
		ResumeExperiencePath(resumeID),
	}
}

func (processor *ModelResumeTailoringExperienceProcessor) Plan(ctx context.Context, input ResumeTailoringInput) (ResumeTailoringPlan, error) {
	if err := input.Validate(); err != nil {
		return ResumeTailoringPlan{}, err
	}
	path := ResumeExperiencePath(input.ResumeID)
	entries, err := observedResumeExperience(input.CurrentState, path)
	if err != nil {
		return ResumeTailoringPlan{}, err
	}
	digest := resumeTailoringExperienceInputDigest(input, path, entries)
	keep := ResumeTailoringPlan{
		ProcessorTag: processor.tag, ProcessorVersion: processor.promptVersion, InputDigest: digest,
	}
	if len(entries) == 0 {
		return keep, nil
	}
	request := ResumeTailoringExperienceRequest{
		Instruction: processor.instruction, PromptVersion: processor.promptVersion,
		VacancyTitle: input.Vacancy.Title, VacancySkills: vacancyAttributeStrings(input.Vacancy, "key_skills"),
		Entries: resumeTailoringExperienceEntries(entries), MaximumRunes: processor.maximumRunes,
	}
	modelCtx, cancel := context.WithTimeout(ctx, processor.timeout)
	response, err := processor.model.RewriteExperience(modelCtx, request)
	cancel()
	if err != nil {
		return keep, nil
	}
	updated, changed, err := applyResumeExperienceRewrite(input, entries, response, processor.maximumRunes)
	if err != nil || !changed {
		return keep, nil
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return ResumeTailoringPlan{}, fmt.Errorf("encode tailored resume experience: %w", err)
	}
	plan := keep
	plan.Overrides = []core.ProfileStateValueOverride{{Path: path, Value: encoded}}
	if err := plan.Validate(input); err != nil {
		return ResumeTailoringPlan{}, err
	}
	return plan, nil
}

func observedResumeExperience(observation core.ProfileStateObservation, path string) ([]map[string]any, error) {
	raw, exists, err := observation.ValueAt(path)
	if err != nil {
		return nil, fmt.Errorf("read observed resume experience: %w", err)
	}
	if !exists || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("observed resume experience is not a JSON array: %w", err)
	}
	return entries, nil
}

func resumeTailoringExperienceEntries(entries []map[string]any) []ResumeTailoringExperienceEntry {
	result := make([]ResumeTailoringExperienceEntry, 0, len(entries))
	for index, entry := range entries {
		id := strings.TrimSpace(experienceString(entry, "id"))
		if id == "" {
			id = fmt.Sprintf("entry-%d", index)
		}
		position := experienceString(entry, "position")
		if position == "" {
			position = experienceString(entry, "title")
		}
		result = append(result, ResumeTailoringExperienceEntry{
			ID: id, Position: position,
			Company:     experienceString(entry, "companyName"),
			Description: experienceString(entry, "description"),
		})
	}
	return result
}

func experienceString(entry map[string]any, key string) string {
	value, exists := entry[key]
	if !exists {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// applyResumeExperienceRewrite substitutes the rewritten descriptions and
// validates every replacement against its own entry text and the vacancy.
// Unknown or duplicate ids, empty or ungrounded text are rejected, and the
// caller then keeps the current resume.
func applyResumeExperienceRewrite(input ResumeTailoringInput, entries []map[string]any, response ResumeTailoringExperienceResponse, maximumRunes int) ([]map[string]any, bool, error) {
	if len(response.Entries) == 0 {
		return nil, false, errors.New("model returned no experience entries")
	}
	originalByID := make(map[string]ResumeTailoringExperienceEntry, len(entries))
	for _, entry := range resumeTailoringExperienceEntries(entries) {
		originalByID[entry.ID] = entry
	}
	rewritten := make(map[string]string, len(response.Entries))
	for _, entry := range response.Entries {
		id := strings.TrimSpace(entry.ID)
		if _, exists := originalByID[id]; !exists {
			return nil, false, fmt.Errorf("model returned an unknown experience id %q", id)
		}
		if _, duplicate := rewritten[id]; duplicate {
			return nil, false, fmt.Errorf("model returned experience id %q twice", id)
		}
		original := originalByID[id]
		description := strings.TrimSpace(entry.Description)
		if description == original.Description {
			continue
		}
		if err := validateResumeExperienceDescription(description, original, input.Vacancy, maximumRunes); err != nil {
			return nil, false, err
		}
		rewritten[id] = description
	}
	if len(rewritten) == 0 {
		return nil, false, nil
	}
	updated := make([]map[string]any, 0, len(entries))
	changed := false
	for index, entry := range entries {
		copied := make(map[string]any, len(entry))
		for key, value := range entry {
			copied[key] = value
		}
		id := strings.TrimSpace(experienceString(entry, "id"))
		if id == "" {
			id = fmt.Sprintf("entry-%d", index)
		}
		if description, exists := rewritten[id]; exists {
			copied["description"] = description
			changed = true
		}
		updated = append(updated, copied)
	}
	return updated, changed, nil
}

func validateResumeExperienceDescription(description string, original ResumeTailoringExperienceEntry, vacancy core.Vacancy, maximumRunes int) error {
	if description == "" {
		return errors.New("model returned an empty experience description")
	}
	if maximumRunes > 0 && utf8.RuneCountInString(description) > maximumRunes {
		return errors.New("experience description is longer than the configured limit")
	}
	if strings.Contains(description, "{{") || strings.Contains(description, "}}") || strings.Contains(description, "```") {
		return errors.New("experience description contains a service artifact")
	}
	trimmed := strings.TrimSpace(description)
	if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(trimmed)) {
		return errors.New("experience description is a structured service response")
	}
	for _, value := range description {
		if unicode.IsControl(value) && value != '\n' && value != '\r' && value != '\t' {
			return errors.New("experience description contains an unsupported control character")
		}
	}
	groundingText := strings.ToLower(strings.Join([]string{
		original.Position, original.Company, original.Description, vacancy.Title,
		strings.Join(vacancyAttributeStrings(vacancy, "key_skills"), "\n"),
	}, "\n"))
	for _, check := range []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{name: "email", pattern: applicationModelEmailPattern},
		{name: "URL", pattern: applicationModelURLPattern},
	} {
		allowed := modelTokenSet(check.pattern, groundingText)
		for token := range modelTokenSet(check.pattern, strings.ToLower(description)) {
			if _, exists := allowed[token]; !exists {
				return fmt.Errorf("experience description contains an ungrounded %s", check.name)
			}
		}
	}
	withoutContacts := applicationModelEmailPattern.ReplaceAllString(description, " ")
	withoutContacts = applicationModelURLPattern.ReplaceAllString(withoutContacts, " ")
	groundingWithoutContacts := applicationModelEmailPattern.ReplaceAllString(groundingText, " ")
	groundingWithoutContacts = applicationModelURLPattern.ReplaceAllString(groundingWithoutContacts, " ")
	allowedNumbers := modelTokenSet(applicationModelNumberPattern, groundingWithoutContacts)
	for number := range modelTokenSet(applicationModelNumberPattern, strings.ToLower(withoutContacts)) {
		if _, exists := allowedNumbers[number]; !exists {
			return errors.New("experience description contains an ungrounded number")
		}
	}
	return nil
}

func resumeTailoringExperienceInputDigest(input ResumeTailoringInput, path string, entries []map[string]any) string {
	encoded, _ := json.Marshal(struct {
		ApplicationID core.ApplicationID `json:"application_id"`
		ProfileID     core.ProfileID     `json:"profile_id"`
		ResumeID      string             `json:"resume_id"`
		Path          string             `json:"path"`
		Entries       []map[string]any   `json:"entries,omitempty"`
	}{
		ApplicationID: input.Application.ID, ProfileID: input.Application.Key.ProfileID,
		ResumeID: input.ResumeID, Path: path, Entries: entries,
	})
	return applicationBytesDigest(encoded)
}
