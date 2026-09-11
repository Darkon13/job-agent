package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
)

const resumeTailoringModelInstruction = `Select resume skills for one application.
Use only skills offered by vacancy.key_skills or already present in resume.skills.
Never remove an existing resume skill. Prefer the vacancy skills that are most relevant to this vacancy.
Return one decision per considered skill with action "add" for a new skill or "keep" for an existing one.
Every decision must carry a non-empty evidence string naming its source.`

// ResumeTailoringModelRequest is the provider-neutral model input. It carries
// only skills and vacancy identity, never baseline or target resume snapshots.
type ResumeTailoringModelRequest struct {
	Instruction   string   `json:"instruction"`
	PromptVersion string   `json:"prompt_version"`
	VacancyTitle  string   `json:"vacancy_title,omitempty"`
	VacancySkills []string `json:"vacancy_skills"`
	CurrentSkills []string `json:"current_skills"`
	MaximumSkills int      `json:"maximum_skills"`
}

type ResumeTailoringModelDecision struct {
	Value    string `json:"value"`
	Action   string `json:"action"`
	Evidence string `json:"evidence"`
}

type ResumeTailoringModelResponse struct {
	Skills     []ResumeTailoringModelDecision `json:"skills"`
	Model      string                         `json:"model,omitempty"`
	ResponseID string                         `json:"response_id,omitempty"`
}

type ResumeTailoringModel interface {
	Select(context.Context, ResumeTailoringModelRequest) (ResumeTailoringModelResponse, error)
}

type ModelResumeTailoringConfig struct {
	Tag           string
	PromptVersion string
	Instruction   string
	MaximumSkills int
	Timeout       time.Duration
	Model         ResumeTailoringModel
	Fallback      ResumeTailoringProcessor
}

// ModelResumeTailoringProcessor asks a model to choose which vacancy skills to
// add and falls back to the deterministic processor on any model failure or
// invalid output. The model never edits the resume directly: every decision is
// validated against the allowed paths, current skills and platform limit.
type ModelResumeTailoringProcessor struct {
	tag           string
	promptVersion string
	instruction   string
	maximumSkills int
	timeout       time.Duration
	model         ResumeTailoringModel
	fallback      ResumeTailoringProcessor
}

func NewModelResumeTailoringProcessor(config ModelResumeTailoringConfig) (*ModelResumeTailoringProcessor, error) {
	processor := &ModelResumeTailoringProcessor{
		tag: strings.TrimSpace(config.Tag), promptVersion: strings.TrimSpace(config.PromptVersion),
		instruction: strings.TrimSpace(config.Instruction), maximumSkills: config.MaximumSkills,
		timeout: config.Timeout, model: config.Model, fallback: config.Fallback,
	}
	if processor.tag == "" || processor.promptVersion == "" || processor.instruction == "" {
		return nil, errors.New("model resume tailoring requires tag, prompt version and instruction")
	}
	if processor.maximumSkills < 1 {
		return nil, errors.New("model resume tailoring requires a positive skill limit")
	}
	if processor.timeout <= 0 {
		return nil, errors.New("model resume tailoring requires a positive timeout")
	}
	if processor.model == nil || processor.fallback == nil {
		return nil, errors.New("model resume tailoring requires a model and deterministic fallback")
	}
	return processor, nil
}

func (processor *ModelResumeTailoringProcessor) Plan(ctx context.Context, input ResumeTailoringInput) (ResumeTailoringPlan, error) {
	if processor == nil {
		return ResumeTailoringPlan{}, errors.New("model resume tailoring processor is nil")
	}
	if err := input.Validate(); err != nil {
		return ResumeTailoringPlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return ResumeTailoringPlan{}, err
	}
	plan, err := processor.planWithModel(ctx, input)
	if err == nil {
		return plan, nil
	}
	return processor.fallback.Plan(ctx, input)
}

func (processor *ModelResumeTailoringProcessor) planWithModel(ctx context.Context, input ResumeTailoringInput) (ResumeTailoringPlan, error) {
	path := ResumeSkillsPath(input.ResumeID)
	if !slices.Contains(input.AllowedPaths, path) {
		return ResumeTailoringPlan{}, fmt.Errorf("resume tailoring skill path %q is not allowed", path)
	}
	current, err := observedResumeSkills(input.CurrentState, path)
	if err != nil {
		return ResumeTailoringPlan{}, err
	}
	modelCtx, cancel := context.WithTimeout(ctx, processor.timeout)
	defer cancel()
	response, err := processor.model.Select(modelCtx, ResumeTailoringModelRequest{
		Instruction:   resumeTailoringModelInstruction + "\n\n" + processor.instruction,
		PromptVersion: processor.promptVersion, VacancyTitle: input.Vacancy.Title,
		VacancySkills: vacancyAttributeStrings(input.Vacancy, "key_skills"),
		CurrentSkills: append([]string(nil), current...), MaximumSkills: processor.maximumSkills,
	})
	if err != nil {
		return ResumeTailoringPlan{}, err
	}
	return processor.planFromDecisions(input, path, current, response.Skills)
}

func (processor *ModelResumeTailoringProcessor) planFromDecisions(input ResumeTailoringInput, path string, current []string, decisions []ResumeTailoringModelDecision) (ResumeTailoringPlan, error) {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("model resume tailoring returned invalid output: "+format, args...)
	}
	if len(decisions) == 0 {
		return ResumeTailoringPlan{}, invalid("model returned no skill decisions")
	}
	allowed := make(map[string]struct{}, len(current)+len(vacancyAttributeStrings(input.Vacancy, "key_skills")))
	for _, skill := range current {
		allowed[normalizeResumeSkill(skill)] = struct{}{}
	}
	for _, skill := range vacancyAttributeStrings(input.Vacancy, "key_skills") {
		allowed[normalizeResumeSkill(skill)] = struct{}{}
	}
	seen := make(map[string]struct{}, len(decisions))
	added := make(map[string]struct{}, len(decisions))
	combined := append([]string(nil), current...)
	selected := make([]ResumeTailoringSkillDecision, 0, len(decisions))
	for _, decision := range decisions {
		value := strings.TrimSpace(decision.Value)
		key := normalizeResumeSkill(value)
		if key == "" || strings.TrimSpace(decision.Evidence) == "" {
			return ResumeTailoringPlan{}, invalid("decision requires value and evidence")
		}
		if _, duplicate := seen[key]; duplicate {
			return ResumeTailoringPlan{}, invalid("duplicate skill decision %q", decision.Value)
		}
		seen[key] = struct{}{}
		if _, known := allowed[key]; !known {
			return ResumeTailoringPlan{}, invalid("skill %q is neither a vacancy skill nor a current skill", decision.Value)
		}
		switch decision.Action {
		case ResumeTailoringSkillAdd, ResumeTailoringSkillKeep:
		default:
			return ResumeTailoringPlan{}, invalid("unsupported action %q", decision.Action)
		}
		action := ResumeTailoringSkillKeep
		if decision.Action == ResumeTailoringSkillAdd && !containsNormalizedResumeSkill(current, key) {
			if _, exists := added[key]; !exists {
				added[key] = struct{}{}
				combined = append(combined, value)
				action = ResumeTailoringSkillAdd
			}
		}
		selected = append(selected, ResumeTailoringSkillDecision{Value: value, Action: action, Evidence: strings.TrimSpace(decision.Evidence)})
	}
	if len(combined) > processor.maximumSkills {
		return ResumeTailoringPlan{}, invalid("selected %d skills above the limit of %d", len(combined), processor.maximumSkills)
	}
	plan := ResumeTailoringPlan{
		ProcessorTag: processor.tag, ProcessorVersion: processor.promptVersion,
		InputDigest: resumeTailoringInputDigest(input, path, current), Skills: selected,
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

func containsNormalizedResumeSkill(values []string, normalized string) bool {
	for _, value := range values {
		if normalizeResumeSkill(value) == normalized {
			return true
		}
	}
	return false
}
