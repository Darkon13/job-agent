package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Darkon13/job-agent/core"
)

const (
	ResumeTailoringSkillAdd  = "add"
	ResumeTailoringSkillKeep = "keep"
)

var ErrResumeTailoringSkillLimit = errors.New("resume tailoring cannot add every vacancy skill without dropping an existing skill")

// ResumeTailoringInput is a private, application-scoped processor input. It
// contains the live platform observation rather than the static profile
// manifest because a successful application must restore this exact state.
type ResumeTailoringInput struct {
	Application    core.Application
	Vacancy        core.Vacancy
	ResumeID       string
	EmployerGroups []string
	CurrentState   core.ProfileStateObservation
	AllowedPaths   []string
}

func (input ResumeTailoringInput) Validate() error {
	if input.Application.ID == "" {
		return errors.New("resume tailoring requires application id")
	}
	if err := input.Application.Key.Validate(); err != nil {
		return err
	}
	if err := input.Vacancy.Validate(); err != nil {
		return err
	}
	if input.Application.Key.Vacancy != input.Vacancy.Key() {
		return errors.New("resume tailoring vacancy does not belong to application")
	}
	if strings.TrimSpace(input.ResumeID) == "" {
		return errors.New("resume tailoring requires resume id")
	}
	if err := input.CurrentState.Validate(); err != nil {
		return err
	}
	if input.CurrentState.ProfileID != input.Application.Key.ProfileID {
		return errors.New("resume tailoring observation belongs to another profile")
	}
	if len(input.AllowedPaths) == 0 {
		return errors.New("resume tailoring requires at least one allowed path")
	}
	seen := make(map[string]struct{}, len(input.AllowedPaths))
	for _, path := range input.AllowedPaths {
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("resume tailoring allowed path %q is not a JSON Pointer", path)
		}
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("resume tailoring contains duplicate allowed path %q", path)
		}
		seen[path] = struct{}{}
	}
	return nil
}

type ResumeTailoringSkillDecision struct {
	Value    string `json:"value"`
	Action   string `json:"action"`
	Evidence string `json:"evidence"`
}

type ResumeTailoringPlan struct {
	ProcessorTag     string                           `json:"processor_tag"`
	ProcessorVersion string                           `json:"processor_version"`
	InputDigest      string                           `json:"input_digest"`
	Overrides        []core.ProfileStateValueOverride `json:"-"`
	Skills           []ResumeTailoringSkillDecision   `json:"skills,omitempty"`
}

func (plan ResumeTailoringPlan) Validate(input ResumeTailoringInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(plan.ProcessorTag) == "" || strings.TrimSpace(plan.ProcessorVersion) == "" || !validResumeTailoringDigest(plan.InputDigest) {
		return errors.New("resume tailoring plan requires processor provenance and input digest")
	}
	allowed := make(map[string]struct{}, len(input.AllowedPaths))
	for _, path := range input.AllowedPaths {
		allowed[path] = struct{}{}
	}
	seenPaths := make(map[string]struct{}, len(plan.Overrides))
	for _, override := range plan.Overrides {
		if _, exists := allowed[override.Path]; !exists {
			return fmt.Errorf("resume tailoring plan changes disallowed path %q", override.Path)
		}
		if _, duplicate := seenPaths[override.Path]; duplicate {
			return fmt.Errorf("resume tailoring plan changes path %q more than once", override.Path)
		}
		seenPaths[override.Path] = struct{}{}
		if len(override.Value) == 0 || !json.Valid(override.Value) {
			return fmt.Errorf("resume tailoring plan contains invalid JSON at %q", override.Path)
		}
	}
	seenSkills := make(map[string]struct{}, len(plan.Skills))
	for _, decision := range plan.Skills {
		key := normalizeResumeSkill(decision.Value)
		if key == "" || strings.TrimSpace(decision.Evidence) == "" {
			return errors.New("resume tailoring skill decision requires value and evidence")
		}
		switch decision.Action {
		case ResumeTailoringSkillAdd, ResumeTailoringSkillKeep:
		default:
			return fmt.Errorf("resume tailoring skill decision has unsupported action %q", decision.Action)
		}
		if _, duplicate := seenSkills[key]; duplicate {
			return fmt.Errorf("resume tailoring plan contains duplicate skill decision %q", decision.Value)
		}
		seenSkills[key] = struct{}{}
	}
	return nil
}

type ResumeTailoringProcessor interface {
	Plan(context.Context, ResumeTailoringInput) (ResumeTailoringPlan, error)
}

// AddVacancySkillsProcessor implements the safe baseline policy: preserve the
// current skill order and append every new vacancy key skill. It refuses to
// truncate either side when the configured platform limit would be exceeded.
type AddVacancySkillsProcessor struct {
	tag           string
	version       string
	maximumSkills int
}

func NewAddVacancySkillsProcessor(tag, version string, maximumSkills int) (*AddVacancySkillsProcessor, error) {
	tag = strings.TrimSpace(tag)
	version = strings.TrimSpace(version)
	if tag == "" || version == "" || maximumSkills < 1 {
		return nil, errors.New("add-vacancy-skills processor requires tag, version and positive skill limit")
	}
	return &AddVacancySkillsProcessor{tag: tag, version: version, maximumSkills: maximumSkills}, nil
}

func (processor *AddVacancySkillsProcessor) Plan(ctx context.Context, input ResumeTailoringInput) (ResumeTailoringPlan, error) {
	if processor == nil {
		return ResumeTailoringPlan{}, errors.New("add-vacancy-skills processor is nil")
	}
	if err := input.Validate(); err != nil {
		return ResumeTailoringPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return ResumeTailoringPlan{}, err
	}
	path := resumeSkillsPath(input.ResumeID)
	if !slices.Contains(input.AllowedPaths, path) {
		return ResumeTailoringPlan{}, fmt.Errorf("resume tailoring skill path %q is not allowed", path)
	}
	current, err := observedResumeSkills(input.CurrentState, path)
	if err != nil {
		return ResumeTailoringPlan{}, err
	}
	if len(current) > processor.maximumSkills {
		return ResumeTailoringPlan{}, fmt.Errorf("current resume contains %d skills, configured maximum is %d", len(current), processor.maximumSkills)
	}

	combined := append([]string(nil), current...)
	seen := make(map[string]struct{}, len(current))
	for _, skill := range current {
		key := normalizeResumeSkill(skill)
		if key == "" {
			return ResumeTailoringPlan{}, errors.New("current resume contains an empty skill")
		}
		if _, duplicate := seen[key]; duplicate {
			return ResumeTailoringPlan{}, fmt.Errorf("current resume contains duplicate skill %q", skill)
		}
		seen[key] = struct{}{}
	}

	decisions := make([]ResumeTailoringSkillDecision, 0)
	for _, candidate := range vacancyAttributeStrings(input.Vacancy, "key_skills") {
		skill := strings.TrimSpace(candidate)
		key := normalizeResumeSkill(skill)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			if !containsResumeSkillDecision(decisions, key) {
				decisions = append(decisions, ResumeTailoringSkillDecision{
					Value: skill, Action: ResumeTailoringSkillKeep, Evidence: "vacancy.key_skills",
				})
			}
			continue
		}
		seen[key] = struct{}{}
		combined = append(combined, skill)
		decisions = append(decisions, ResumeTailoringSkillDecision{
			Value: skill, Action: ResumeTailoringSkillAdd, Evidence: "vacancy.key_skills",
		})
	}
	if len(combined) > processor.maximumSkills {
		return ResumeTailoringPlan{}, fmt.Errorf("%w: need %d slots, maximum is %d", ErrResumeTailoringSkillLimit, len(combined), processor.maximumSkills)
	}

	plan := ResumeTailoringPlan{
		ProcessorTag: processor.tag, ProcessorVersion: processor.version,
		InputDigest: resumeTailoringInputDigest(input, path, current), Skills: decisions,
	}
	if len(combined) != len(current) {
		encoded, err := json.Marshal(combined)
		if err != nil {
			return ResumeTailoringPlan{}, fmt.Errorf("encode tailored resume skills: %w", err)
		}
		plan.Overrides = []core.ProfileStateValueOverride{{Path: path, Value: encoded}}
	}
	if err := plan.Validate(input); err != nil {
		return ResumeTailoringPlan{}, err
	}
	return plan, nil
}

func observedResumeSkills(observation core.ProfileStateObservation, path string) ([]string, error) {
	raw, exists, err := observation.ValueAt(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("observed resume state does not contain restorable skill path %q", path)
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("observed resume skills at %q must be a string array: %w", path, err)
	}
	return values, nil
}

func resumeSkillsPath(resumeID string) string {
	return "/resumes/" + escapeResumeTailoringPointer(strings.TrimSpace(resumeID)) + "/web/keySkills"
}

func escapeResumeTailoringPointer(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

func normalizeResumeSkill(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func containsResumeSkillDecision(decisions []ResumeTailoringSkillDecision, normalized string) bool {
	for _, decision := range decisions {
		if normalizeResumeSkill(decision.Value) == normalized {
			return true
		}
	}
	return false
}

func resumeTailoringInputDigest(input ResumeTailoringInput, skillPath string, current []string) string {
	encoded, _ := json.Marshal(struct {
		ApplicationID  core.ApplicationID `json:"application_id"`
		ProfileID      core.ProfileID     `json:"profile_id"`
		Vacancy        core.VacancyKey    `json:"vacancy"`
		VacancySkills  []string           `json:"vacancy_skills"`
		Employer       string             `json:"employer"`
		EmployerGroups []string           `json:"employer_groups"`
		ResumeID       string             `json:"resume_id"`
		StateDigest    string             `json:"state_digest"`
		SkillPath      string             `json:"skill_path"`
		CurrentSkills  []string           `json:"current_skills"`
	}{
		ApplicationID: input.Application.ID, ProfileID: input.Application.Key.ProfileID,
		Vacancy: input.Vacancy.Key(), VacancySkills: vacancyAttributeStrings(input.Vacancy, "key_skills"),
		Employer: input.Vacancy.Employer, EmployerGroups: input.EmployerGroups,
		ResumeID: input.ResumeID, StateDigest: input.CurrentState.StateDigest,
		SkillPath: skillPath, CurrentSkills: current,
	})
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validResumeTailoringDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}
