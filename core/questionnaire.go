package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// QuestionKind describes how a platform expects a questionnaire answer.
type QuestionKind string

const (
	QuestionSingle   QuestionKind = "single"
	QuestionMultiple QuestionKind = "multiple"
	QuestionText     QuestionKind = "text"
	// QuestionCode is discoverable and persistable, but is intentionally not
	// supported by the automatic answer/submission pipeline yet.
	QuestionCode QuestionKind = "code"
)

// QuestionOption keeps the platform ID separate from the stable display text.
// Platform IDs may change between otherwise identical questionnaire instances.
type QuestionOption struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Question is one runtime questionnaire item extracted by an adapter.
type Question struct {
	ID      string           `json:"id"`
	Text    string           `json:"text"`
	Kind    QuestionKind     `json:"kind"`
	Options []QuestionOption `json:"options,omitempty"`
}

// Questionnaire is ordered as presented by the platform. Fingerprint ignores
// question and option order.
type Questionnaire struct {
	Title     string     `json:"title,omitempty"`
	Questions []Question `json:"questions"`
}

// StoredAnswer is portable between questionnaire instances because it refers
// to question and option text rather than platform-specific IDs or positions.
type StoredAnswer struct {
	Question            string            `json:"question"`
	QuestionFingerprint string            `json:"question_fingerprint,omitempty"`
	SelectedOptions     []string          `json:"selected_options,omitempty"`
	Text                string            `json:"text,omitempty"`
	Provenance          *AnswerProvenance `json:"provenance,omitempty"`
}

// AnswerProvenance records how a stored answer was produced. Hand-written
// reviewed answers may omit it; model answers carry the provider metadata and
// stay unverified until the platform result or a human confirms them.
type AnswerProvenance struct {
	Resolver      string `json:"resolver"`
	ModelTag      string `json:"model_tag,omitempty"`
	ProviderModel string `json:"provider_model,omitempty"`
	PromptVersion string `json:"prompt_version,omitempty"`
	ResponseID    string `json:"response_id,omitempty"`
	InputDigest   string `json:"input_digest,omitempty"`
	OutputDigest  string `json:"output_digest,omitempty"`
	Confidence    string `json:"confidence,omitempty"`
	Verified      bool   `json:"verified,omitempty"`
}

const (
	AnswerResolverModel     = "model"
	AnswerResolverHuman     = "human"
	AnswerResolverKnown     = "known_answer"
	AnswerResolverStudyBank = "study_bank"
	AnswerConfidenceHigh    = "high"
	AnswerConfidenceMedium  = "medium"
	AnswerConfidenceLow     = "low"
)

func (provenance *AnswerProvenance) Validate() error {
	if provenance == nil {
		return nil
	}
	switch provenance.Resolver {
	case AnswerResolverModel, AnswerResolverHuman, AnswerResolverKnown, AnswerResolverStudyBank:
	default:
		return fmt.Errorf("answer provenance has unsupported resolver %q", provenance.Resolver)
	}
	if provenance.Resolver == AnswerResolverModel {
		if strings.TrimSpace(provenance.ModelTag) == "" || strings.TrimSpace(provenance.ProviderModel) == "" ||
			strings.TrimSpace(provenance.PromptVersion) == "" {
			return errors.New("model answer provenance requires model tag, provider model and prompt version")
		}
	}
	if provenance.Confidence != "" {
		switch provenance.Confidence {
		case AnswerConfidenceHigh, AnswerConfidenceMedium, AnswerConfidenceLow:
		default:
			return fmt.Errorf("answer provenance has unsupported confidence %q", provenance.Confidence)
		}
	}
	return nil
}

func cloneAnswerProvenance(provenance *AnswerProvenance) *AnswerProvenance {
	if provenance == nil {
		return nil
	}
	copied := *provenance
	return &copied
}

type AnswerBlockKind string

const (
	AnswerBlockQualification AnswerBlockKind = "qualification"
	AnswerBlockConversation  AnswerBlockKind = "conversation"
	// AnswerBlockVacancy is the reviewed question bank of vacancy popup tests.
	// It is matched by platform only: such tests expose no family or level.
	AnswerBlockVacancy AnswerBlockKind = "vacancy"
)

// QualificationReviewedBlockTag is the durable block tag human reviews and
// platform-verified model answers extend when a level has no declarative block.
func QualificationReviewedBlockTag(platform Platform, familyID, levelID string) string {
	return string(platform) + "-" + familyID + "-" + levelID + "-reviewed"
}

type AnswerBlockMatcher struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	Topic       string `json:"topic,omitempty"`
}

// AnswerBlock is one small, logically named group of reviewed answers. Files
// contain one block; the config builder assembles blocks into a runtime registry.
type AnswerBlock struct {
	Tag           string                   `json:"tag"`
	Name          string                   `json:"name"`
	Kind          AnswerBlockKind          `json:"kind"`
	Platform      Platform                 `json:"platform"`
	Qualification *QualificationDescriptor `json:"qualification,omitempty"`
	Match         AnswerBlockMatcher       `json:"match,omitempty"`
	Answers       []StoredAnswer           `json:"answers"`
}

// ResolvedAnswer contains only runtime IDs and is ready for an adapter preview.
// It deliberately does not submit anything to the platform.
type ResolvedAnswer struct {
	QuestionID        string   `json:"question_id"`
	SelectedOptionIDs []string `json:"selected_option_ids,omitempty"`
	Text              string   `json:"text,omitempty"`
}

func (answer ResolvedAnswer) Validate() error {
	if strings.TrimSpace(answer.QuestionID) == "" {
		return errors.New("resolved answer requires question id")
	}
	if len(answer.SelectedOptionIDs) == 0 && strings.TrimSpace(answer.Text) == "" {
		return fmt.Errorf("resolved answer for question %q requires text or selected options", answer.QuestionID)
	}
	if len(answer.SelectedOptionIDs) != 0 && strings.TrimSpace(answer.Text) != "" {
		return fmt.Errorf("resolved answer for question %q mixes text and selected options", answer.QuestionID)
	}
	for _, optionID := range answer.SelectedOptionIDs {
		if strings.TrimSpace(optionID) == "" {
			return fmt.Errorf("resolved answer for question %q contains an empty option", answer.QuestionID)
		}
	}
	return nil
}

// AnswerPlan is the strict result of matching a reviewed answer block against a
// concrete questionnaire instance.
type AnswerPlan struct {
	AnswerBlockTag     string           `json:"answer_block_tag"`
	AnswerBlockName    string           `json:"answer_block_name"`
	AttemptFingerprint string           `json:"attempt_fingerprint"`
	Answers            []ResolvedAnswer `json:"answers"`
}

// NormalizeQuestionText normalizes presentation-only differences without
// attempting fuzzy or semantic matching.
func NormalizeQuestionText(value string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(strings.TrimSpace(value)), func(r rune) bool {
		return unicode.IsSpace(r)
	}), " ")
}

// QuestionFingerprint identifies one complete question independently of its
// runtime ID and the order in which its options are displayed.
func QuestionFingerprint(question Question) (string, error) {
	if err := validateQuestion(question); err != nil {
		return "", err
	}
	options := make([]string, 0, len(question.Options))
	for _, option := range question.Options {
		options = append(options, NormalizeQuestionText(option.Text))
	}
	sort.Strings(options)
	signature := strings.Join([]string{
		string(question.Kind),
		NormalizeQuestionText(question.Text),
		strings.Join(options, "\x1f"),
	}, "\x1e")
	sum := sha256.Sum256([]byte(signature))
	return hex.EncodeToString(sum[:]), nil
}

// ObservedAttemptFingerprint is an observed-attempt fingerprint. It is stable
// when questions/options are reordered, but it is not available before a
// progressive test ends and must not gate per-question answer reuse.
func ObservedAttemptFingerprint(questionnaire Questionnaire) (string, error) {
	if err := validateQuestionnaire(questionnaire); err != nil {
		return "", err
	}

	signatures := make([]string, 0, len(questionnaire.Questions))
	for _, question := range questionnaire.Questions {
		fingerprint, err := QuestionFingerprint(question)
		if err != nil {
			return "", err
		}
		signatures = append(signatures, fingerprint)
	}
	sort.Strings(signatures)

	sum := sha256.Sum256([]byte(strings.Join(signatures, "\x1d")))
	return hex.EncodeToString(sum[:]), nil
}

// QuestionnaireFingerprint is kept as a compatibility alias for imports and
// static mocks. New workflows should use ObservedAttemptFingerprint explicitly.
func QuestionnaireFingerprint(questionnaire Questionnaire) (string, error) {
	return ObservedAttemptFingerprint(questionnaire)
}

// ResolveQuestionAnswer resolves one progressively revealed question. Unlike
// ResolveAnswerBlock it requires a per-question fingerprint and never relies on
// a not-yet-known full attempt manifest.
func ResolveQuestionAnswer(question Question, block AnswerBlock) (ResolvedAnswer, error) {
	if err := ValidateAnswerBlock(block); err != nil {
		return ResolvedAnswer{}, err
	}
	if block.Kind != AnswerBlockQualification {
		return ResolvedAnswer{}, fmt.Errorf("answer block %q is not a qualification block", block.Tag)
	}
	fingerprint, err := QuestionFingerprint(question)
	if err != nil {
		return ResolvedAnswer{}, err
	}
	for _, answer := range block.Answers {
		if answer.QuestionFingerprint == fingerprint {
			return resolveStoredAnswer(question, answer)
		}
	}
	return ResolvedAnswer{}, fmt.Errorf("answer block %q has no reviewed answer for question fingerprint %s", block.Tag, fingerprint)
}

// ResolveStoredAnswer resolves one runtime question from a reviewed answer.
// Unlike ResolveQuestionAnswer it does not require a portable block and is used
// for human review selections whose question is already at hand.
func ResolveStoredAnswer(question Question, answer StoredAnswer) (ResolvedAnswer, error) {
	return resolveStoredAnswer(question, answer)
}

// ResolveAnswerBlock strictly maps user-supplied answer text to runtime question
// and option IDs. It rejects missing, extra, duplicate, changed, or ambiguous
// content instead of guessing.
func ResolveAnswerBlock(questionnaire Questionnaire, block AnswerBlock) (AnswerPlan, error) {
	fingerprint, err := QuestionnaireFingerprint(questionnaire)
	if err != nil {
		return AnswerPlan{}, err
	}
	if err := ValidateAnswerBlock(block); err != nil {
		return AnswerPlan{}, err
	}
	if block.Kind != AnswerBlockQualification && block.Kind != AnswerBlockVacancy {
		return AnswerPlan{}, fmt.Errorf("answer block %q is not a qualification or vacancy block", block.Tag)
	}
	stored := make(map[string]StoredAnswer, len(block.Answers))
	for _, answer := range block.Answers {
		key := NormalizeQuestionText(answer.Question)
		if key == "" {
			return AnswerPlan{}, errors.New("answer block contains an empty question")
		}
		if _, exists := stored[key]; exists {
			return AnswerPlan{}, fmt.Errorf("answer block contains duplicate question %q", answer.Question)
		}
		stored[key] = answer
	}

	plan := AnswerPlan{
		AnswerBlockTag: block.Tag, AnswerBlockName: block.Name,
		AttemptFingerprint: fingerprint, Answers: make([]ResolvedAnswer, 0, len(questionnaire.Questions)),
	}
	used := make(map[string]struct{}, len(stored))
	for _, question := range questionnaire.Questions {
		key := NormalizeQuestionText(question.Text)
		answer, exists := stored[key]
		if !exists {
			return AnswerPlan{}, fmt.Errorf("no stored answer for question %q", question.Text)
		}
		used[key] = struct{}{}

		if answer.QuestionFingerprint != "" {
			questionFingerprint, err := QuestionFingerprint(question)
			if err != nil {
				return AnswerPlan{}, err
			}
			if answer.QuestionFingerprint != questionFingerprint {
				return AnswerPlan{}, fmt.Errorf("stored answer fingerprint mismatch for question %q", question.Text)
			}
		}
		resolved, err := resolveStoredAnswer(question, answer)
		if err != nil {
			return AnswerPlan{}, err
		}
		plan.Answers = append(plan.Answers, resolved)
	}

	if len(used) != len(stored) {
		for key, answer := range stored {
			if _, exists := used[key]; !exists {
				return AnswerPlan{}, fmt.Errorf("stored answer references unknown question %q", answer.Question)
			}
		}
	}
	return plan, nil
}

// UncoveredQuestions lists the questionnaire questions that the answer block
// has no reviewed answer for. Matching mirrors ResolveAnswerBlock: normalized
// question text first, then the stored fingerprint when present.
func UncoveredQuestions(questionnaire Questionnaire, block AnswerBlock) ([]Question, error) {
	if err := ValidateAnswerBlock(block); err != nil {
		return nil, err
	}
	stored := make(map[string]StoredAnswer, len(block.Answers))
	for _, answer := range block.Answers {
		key := NormalizeQuestionText(answer.Question)
		if key == "" {
			return nil, errors.New("answer block contains an empty question")
		}
		if _, exists := stored[key]; exists {
			return nil, fmt.Errorf("answer block contains duplicate question %q", answer.Question)
		}
		stored[key] = answer
	}
	var missing []Question
	for _, question := range questionnaire.Questions {
		answer, exists := stored[NormalizeQuestionText(question.Text)]
		if !exists {
			missing = append(missing, question)
			continue
		}
		if answer.QuestionFingerprint != "" {
			fingerprint, err := QuestionFingerprint(question)
			if err != nil {
				return nil, err
			}
			if fingerprint != answer.QuestionFingerprint {
				missing = append(missing, question)
			}
		}
	}
	return missing, nil
}

// ValidateAnswerBlock validates the portable JSON object without requiring a
// concrete platform questionnaire.
func ValidateAnswerBlock(block AnswerBlock) error {
	if block.Tag == "" || block.Name == "" {
		return errors.New("answer block requires tag and name")
	}
	if block.Platform == "" {
		return errors.New("answer block requires platform")
	}
	switch block.Kind {
	case AnswerBlockQualification:
		if block.Match.Topic != "" || block.Match.Fingerprint != "" {
			return errors.New("qualification answer block must use qualification and per-question matchers")
		}
		if block.Qualification != nil {
			if err := validateQualificationDescriptor(*block.Qualification); err != nil {
				return err
			}
		}
	case AnswerBlockConversation:
		if block.Qualification != nil {
			return errors.New("conversation answer block must not contain qualification metadata")
		}
		if block.Match.Fingerprint != "" && block.Match.Topic != "" {
			return errors.New("conversation answer block must use at most one matcher")
		}
	case AnswerBlockVacancy:
		if block.Qualification != nil {
			return errors.New("vacancy answer block must not contain qualification metadata")
		}
		if block.Match.Fingerprint != "" || block.Match.Topic != "" {
			return errors.New("vacancy answer block must not use matchers")
		}
	default:
		return fmt.Errorf("answer block has unsupported kind %q", block.Kind)
	}
	if len(block.Answers) == 0 {
		return errors.New("answer block requires at least one answer")
	}
	if block.Match.Fingerprint != "" {
		if err := validateSHA256Fingerprint(block.Match.Fingerprint); err != nil {
			return fmt.Errorf("answer block matcher: %w", err)
		}
	}

	seen := make(map[string]struct{}, len(block.Answers))
	for _, answer := range block.Answers {
		questionText := NormalizeQuestionText(answer.Question)
		if questionText == "" {
			return errors.New("answer block contains an empty question")
		}
		key := "text:" + questionText
		if answer.QuestionFingerprint != "" {
			if err := validateSHA256Fingerprint(answer.QuestionFingerprint); err != nil {
				return fmt.Errorf("answer for question %q: %w", answer.Question, err)
			}
			key = "fingerprint:" + answer.QuestionFingerprint
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("answer block contains duplicate question %q", answer.Question)
		}
		seen[key] = struct{}{}
		if answer.Text == "" && len(answer.SelectedOptions) == 0 {
			return fmt.Errorf("answer for question %q is empty", answer.Question)
		}
		if answer.Text != "" && len(answer.SelectedOptions) != 0 {
			return fmt.Errorf("answer for question %q mixes text and selected options", answer.Question)
		}
		if err := answer.Provenance.Validate(); err != nil {
			return fmt.Errorf("answer for question %q: %w", answer.Question, err)
		}
	}
	return nil
}

func resolveStoredAnswer(question Question, answer StoredAnswer) (ResolvedAnswer, error) {
	resolved := ResolvedAnswer{QuestionID: question.ID}
	switch question.Kind {
	case QuestionCode:
		return ResolvedAnswer{}, fmt.Errorf("question %q is a code task and requires an unsupported handler", question.Text)
	case QuestionText:
		if answer.Text == "" || len(answer.SelectedOptions) != 0 {
			return ResolvedAnswer{}, fmt.Errorf("text question %q requires text only", question.Text)
		}
		resolved.Text = answer.Text
	case QuestionSingle, QuestionMultiple:
		if answer.Text != "" {
			return ResolvedAnswer{}, fmt.Errorf("choice question %q must not contain text answer", question.Text)
		}
		if question.Kind == QuestionSingle && len(answer.SelectedOptions) != 1 {
			return ResolvedAnswer{}, fmt.Errorf("single-choice question %q requires exactly one option", question.Text)
		}
		if question.Kind == QuestionMultiple && len(answer.SelectedOptions) == 0 {
			return ResolvedAnswer{}, fmt.Errorf("multiple-choice question %q requires at least one option", question.Text)
		}
		optionIDs, err := resolveOptions(question, answer.SelectedOptions)
		if err != nil {
			return ResolvedAnswer{}, err
		}
		resolved.SelectedOptionIDs = optionIDs
	default:
		return ResolvedAnswer{}, fmt.Errorf("question %q has unsupported kind %q", question.Text, question.Kind)
	}
	return resolved, nil
}

func validateSHA256Fingerprint(fingerprint string) error {
	decoded, err := hex.DecodeString(fingerprint)
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("fingerprint must be a SHA-256 hex string")
	}
	return nil
}

func resolveOptions(question Question, selected []string) ([]string, error) {
	options := make(map[string]string, len(question.Options))
	for _, option := range question.Options {
		key := NormalizeQuestionText(option.Text)
		if _, exists := options[key]; exists {
			return nil, fmt.Errorf("question %q contains duplicate option %q", question.Text, option.Text)
		}
		options[key] = option.ID
	}

	result := make([]string, 0, len(selected))
	seen := make(map[string]struct{}, len(selected))
	for _, selectedText := range selected {
		key := NormalizeQuestionText(selectedText)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("question %q selects option %q more than once", question.Text, selectedText)
		}
		seen[key] = struct{}{}
		id, exists := options[key]
		if !exists {
			return nil, fmt.Errorf("question %q has no option %q", question.Text, selectedText)
		}
		result = append(result, id)
	}
	return result, nil
}

func validateQuestionnaire(questionnaire Questionnaire) error {
	if len(questionnaire.Questions) == 0 {
		return errors.New("questionnaire requires at least one question")
	}
	questions := make(map[string]struct{}, len(questionnaire.Questions))
	for _, question := range questionnaire.Questions {
		if err := validateQuestion(question); err != nil {
			return err
		}
		key := NormalizeQuestionText(question.Text)
		if _, exists := questions[key]; exists {
			return fmt.Errorf("questionnaire contains duplicate question %q", question.Text)
		}
		questions[key] = struct{}{}
	}
	return nil
}

func validateQuestion(question Question) error {
	if question.ID == "" {
		return errors.New("question requires runtime id")
	}
	if NormalizeQuestionText(question.Text) == "" {
		return fmt.Errorf("question %q has empty text", question.ID)
	}
	switch question.Kind {
	case QuestionText, QuestionCode:
		if len(question.Options) != 0 {
			return fmt.Errorf("%s question %q must not contain options", question.Kind, question.Text)
		}
	case QuestionSingle, QuestionMultiple:
		if len(question.Options) == 0 {
			return fmt.Errorf("choice question %q requires options", question.Text)
		}
		seen := make(map[string]struct{}, len(question.Options))
		for _, option := range question.Options {
			if option.ID == "" || NormalizeQuestionText(option.Text) == "" {
				return fmt.Errorf("choice question %q contains an option without runtime id or text", question.Text)
			}
			key := NormalizeQuestionText(option.Text)
			if _, exists := seen[key]; exists {
				return fmt.Errorf("question %q contains duplicate option %q", question.Text, option.Text)
			}
			seen[key] = struct{}{}
		}
	default:
		return fmt.Errorf("question %q has unsupported kind %q", question.Text, question.Kind)
	}
	return nil
}
