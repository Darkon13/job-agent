package questionbank

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Darkon13/job-agent/core"
)

const (
	SchemaVersion               = 1
	VerificationExternalUnknown = "external_unverified"
)

// Source identifies the exact material from which a study bank was imported.
// A study bank is evidence for review, not a verified platform answer block.
type Source struct {
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Path       string `json:"path"`
	License    string `json:"license"`
}

// Question retains every option present in the source and explicitly records
// whether that list was complete. SuggestedOptions are intentionally not named
// "correct": an external checklist is not trusted platform evidence.
type Question struct {
	SourceID         string            `json:"source_id"`
	Text             string            `json:"text"`
	Kind             core.QuestionKind `json:"kind"`
	Options          []string          `json:"options"`
	SuggestedOptions []string          `json:"suggested_options"`
	OptionsComplete  bool              `json:"options_complete"`
}

// Bank is an importable practice artifact. Platform is normally "study" so a
// third-party answer list cannot accidentally enter the HH reusable registry.
type Bank struct {
	SchemaVersion  int                          `json:"schema_version"`
	Tag            string                       `json:"tag"`
	Name           string                       `json:"name"`
	Platform       core.Platform                `json:"platform"`
	TargetPlatform core.Platform                `json:"target_platform"`
	Qualification  core.QualificationDescriptor `json:"qualification"`
	Verification   string                       `json:"verification"`
	Source         Source                       `json:"source"`
	Questions      []Question                   `json:"questions"`
}

type PracticeFixture struct {
	Questionnaire core.Questionnaire `json:"questionnaire"`
	AnswerBlock   core.AnswerBlock   `json:"answer_block"`
	Skipped       int                `json:"skipped_incomplete_questions"`
}

func ReadFile(path string) (Bank, error) {
	file, err := os.Open(path)
	if err != nil {
		return Bank{}, fmt.Errorf("open study bank: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var bank Bank
	if err := decoder.Decode(&bank); err != nil {
		return Bank{}, fmt.Errorf("decode study bank: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Bank{}, errors.New("decode study bank: unexpected trailing JSON value")
	} else if err != io.EOF {
		return Bank{}, fmt.Errorf("decode study bank trailing data: %w", err)
	}
	if err := bank.Validate(); err != nil {
		return Bank{}, err
	}
	return bank, nil
}

func (bank Bank) Validate() error {
	if bank.SchemaVersion != SchemaVersion {
		return fmt.Errorf("study bank schema version must be %d", SchemaVersion)
	}
	if strings.TrimSpace(bank.Tag) == "" || strings.TrimSpace(bank.Name) == "" {
		return errors.New("study bank requires tag and name")
	}
	if bank.Platform != "study" {
		return errors.New("external study bank platform must be study")
	}
	if bank.TargetPlatform == "" {
		return errors.New("study bank requires target platform")
	}
	if bank.Verification != VerificationExternalUnknown {
		return fmt.Errorf("unsupported study bank verification %q", bank.Verification)
	}
	if strings.TrimSpace(bank.Qualification.FamilyID) == "" || strings.TrimSpace(bank.Qualification.FamilyName) == "" ||
		strings.TrimSpace(bank.Qualification.LevelID) == "" || strings.TrimSpace(bank.Qualification.LevelName) == "" {
		return errors.New("study bank requires qualification family and level")
	}
	if strings.TrimSpace(bank.Source.Repository) == "" || strings.TrimSpace(bank.Source.Revision) == "" ||
		strings.TrimSpace(bank.Source.Path) == "" || strings.TrimSpace(bank.Source.License) == "" {
		return errors.New("study bank requires complete source provenance")
	}
	if len(bank.Questions) == 0 {
		return errors.New("study bank requires at least one question")
	}

	seen := make(map[string]struct{}, len(bank.Questions))
	for _, question := range bank.Questions {
		if strings.TrimSpace(question.SourceID) == "" {
			return errors.New("study bank contains a question without source id")
		}
		if _, exists := seen[question.SourceID]; exists {
			return fmt.Errorf("study bank contains duplicate source id %q", question.SourceID)
		}
		seen[question.SourceID] = struct{}{}
		if core.NormalizeQuestionText(question.Text) == "" {
			return errors.New("study bank contains an empty question")
		}
		if question.Kind != core.QuestionSingle && question.Kind != core.QuestionMultiple {
			return fmt.Errorf("study question %q has unsupported kind %q", question.Text, question.Kind)
		}
		if len(question.SuggestedOptions) == 0 {
			return fmt.Errorf("study question %q has no suggested option", question.Text)
		}
		if question.Kind == core.QuestionSingle && len(question.SuggestedOptions) != 1 {
			return fmt.Errorf("single-choice study question %q must have one suggested option", question.Text)
		}
		if question.OptionsComplete && len(question.Options) <= len(question.SuggestedOptions) {
			return fmt.Errorf("complete study question %q must contain alternative options", question.Text)
		}
		if err := validateOptions(question); err != nil {
			return err
		}
	}
	return nil
}

func validateOptions(question Question) error {
	available := make(map[string]struct{}, len(question.Options))
	for _, option := range question.Options {
		key := core.NormalizeQuestionText(option)
		if key == "" {
			return fmt.Errorf("study question %q contains an empty option", question.Text)
		}
		if _, exists := available[key]; exists {
			return fmt.Errorf("study question %q contains duplicate option %q", question.Text, option)
		}
		available[key] = struct{}{}
	}
	for _, suggested := range question.SuggestedOptions {
		if _, exists := available[core.NormalizeQuestionText(suggested)]; !exists {
			return fmt.Errorf("study question %q suggests an unavailable option %q", question.Text, suggested)
		}
	}
	return nil
}

// BuildPracticeFixture promotes only questions whose complete option list is
// present. The resulting block remains scoped to platform "study".
func BuildPracticeFixture(bank Bank) (PracticeFixture, error) {
	if err := bank.Validate(); err != nil {
		return PracticeFixture{}, err
	}

	qualification := bank.Qualification
	fixture := PracticeFixture{
		Questionnaire: core.Questionnaire{Title: bank.Name},
		AnswerBlock: core.AnswerBlock{
			Tag: bank.Tag, Name: bank.Name, Kind: core.AnswerBlockQualification,
			Platform: bank.Platform, Qualification: &qualification,
		},
	}
	textCounts := make(map[string]int, len(bank.Questions))
	for _, candidate := range bank.Questions {
		if candidate.OptionsComplete {
			textCounts[core.NormalizeQuestionText(candidate.Text)]++
		}
	}
	for index, candidate := range bank.Questions {
		if !candidate.OptionsComplete || textCounts[core.NormalizeQuestionText(candidate.Text)] != 1 {
			fixture.Skipped++
			continue
		}
		question := core.Question{
			ID: fmt.Sprintf("study-question-%d", index+1), Text: candidate.Text, Kind: candidate.Kind,
		}
		for optionIndex, option := range candidate.Options {
			question.Options = append(question.Options, core.QuestionOption{
				ID: fmt.Sprintf("study-option-%d-%d", index+1, optionIndex+1), Text: option,
			})
		}
		fingerprint, err := core.QuestionFingerprint(question)
		if err != nil {
			return PracticeFixture{}, err
		}
		fixture.Questionnaire.Questions = append(fixture.Questionnaire.Questions, question)
		fixture.AnswerBlock.Answers = append(fixture.AnswerBlock.Answers, core.StoredAnswer{
			Question: candidate.Text, QuestionFingerprint: fingerprint,
			SelectedOptions: append([]string(nil), candidate.SuggestedOptions...),
		})
	}
	if len(fixture.Questionnaire.Questions) == 0 {
		return PracticeFixture{}, errors.New("study bank has no questions with complete option lists")
	}
	if err := core.ValidateAnswerBlock(fixture.AnswerBlock); err != nil {
		return PracticeFixture{}, err
	}
	return fixture, nil
}
