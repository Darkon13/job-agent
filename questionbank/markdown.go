package questionbank

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Darkon13/job-agent/core"
)

var (
	headingPattern = regexp.MustCompile(`^####\s+(.+?)\s*$`)
	optionPattern  = regexp.MustCompile(`^\s*[-*]\s+\[([xX ])\]\s+(.+?)\s*$`)
	questionNumber = regexp.MustCompile(`^Q\d+\.\s*`)
	markdownLink   = regexp.MustCompile(`\[([^]]+)\]\([^)]+\)`)
)

type ImportMetadata struct {
	Tag            string
	Name           string
	Platform       core.Platform
	TargetPlatform core.Platform
	Qualification  core.QualificationDescriptor
	Source         Source
}

type ImportReport struct {
	Questions           int
	CompleteQuestions   int
	IncompleteQuestions int
	IgnoredSections     int
}

type markdownOption struct {
	text         string
	selected     bool
	continuation bool
}

type markdownSection struct {
	sourceID string
	heading  string
	body     []string
	options  []markdownOption
}

func ParseMarkdown(reader io.Reader, metadata ImportMetadata) (Bank, ImportReport, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var title string
	var current *markdownSection
	var sections []markdownSection
	headingIndex := 0
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if title == "" && strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "### ") {
			title = cleanMarkdown(strings.TrimPrefix(line, "## "))
		}
		if match := headingPattern.FindStringSubmatch(line); match != nil {
			if current != nil {
				sections = append(sections, *current)
			}
			headingIndex++
			current = &markdownSection{sourceID: sourceID(match[1], headingIndex), heading: match[1]}
			continue
		}
		if current == nil {
			continue
		}
		if match := optionPattern.FindStringSubmatch(line); match != nil {
			text := cleanMarkdown(match[2])
			current.options = append(current.options, markdownOption{
				text: text, selected: match[1] == "x" || match[1] == "X", continuation: text == "↓",
			})
			continue
		}
		if len(current.options) == 0 {
			current.body = append(current.body, line)
		} else {
			appendOptionContinuation(&current.options[len(current.options)-1], line)
		}
	}
	if err := scanner.Err(); err != nil {
		return Bank{}, ImportReport{}, fmt.Errorf("scan markdown: %w", err)
	}
	if current != nil {
		sections = append(sections, *current)
	}

	name := strings.TrimSpace(metadata.Name)
	if name == "" {
		name = title
	}
	qualification := metadata.Qualification
	if qualification.FamilyName == "" || qualification.LevelName == "" {
		familyName, levelName := qualificationTitleParts(title)
		if qualification.FamilyName == "" {
			qualification.FamilyName = familyName
		}
		if qualification.LevelName == "" {
			qualification.LevelName = levelName
		}
	}
	bank := Bank{
		SchemaVersion: SchemaVersion, Tag: metadata.Tag, Name: name,
		Platform: metadata.Platform, TargetPlatform: metadata.TargetPlatform,
		Qualification: qualification, Verification: VerificationExternalUnknown,
		Source: metadata.Source,
	}
	var report ImportReport
	for _, section := range sections {
		question, ok := sectionQuestion(section)
		if !ok {
			report.IgnoredSections++
			continue
		}
		bank.Questions = append(bank.Questions, question)
		report.Questions++
		if question.OptionsComplete {
			report.CompleteQuestions++
		} else {
			report.IncompleteQuestions++
		}
	}
	if err := bank.Validate(); err != nil {
		return Bank{}, report, err
	}
	return bank, report, nil
}

func appendOptionContinuation(option *markdownOption, line string) {
	if option == nil || !option.continuation {
		return
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "```") {
		return
	}
	if option.text == "↓" {
		option.text = trimmed
		return
	}
	option.text += "\n" + trimmed
}

func qualificationTitleParts(title string) (string, string) {
	for _, separator := range []string{" — ", " – ", " - "} {
		parts := strings.SplitN(title, separator, 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
	}
	return strings.TrimSpace(title), "не указан"
}

func sectionQuestion(section markdownSection) (Question, bool) {
	selected := make([]string, 0, len(section.options))
	options := make([]string, 0, len(section.options))
	hasAlternative := false
	for _, option := range section.options {
		options = append(options, option.text)
		if option.selected {
			selected = append(selected, option.text)
		} else {
			hasAlternative = true
		}
	}
	if len(selected) == 0 {
		return Question{}, false
	}

	parts := []string{questionNumber.ReplaceAllString(cleanMarkdown(section.heading), "")}
	if body := cleanBlock(section.body); body != "" {
		parts = append(parts, body)
	}
	kind := core.QuestionSingle
	if len(selected) > 1 {
		kind = core.QuestionMultiple
	}
	return Question{
		SourceID: section.sourceID, Text: strings.Join(parts, "\n\n"), Kind: kind, Options: options,
		SuggestedOptions: selected, OptionsComplete: hasAlternative,
	}, true
}

func sourceID(heading string, index int) string {
	trimmed := strings.TrimSpace(heading)
	if end := strings.Index(trimmed, "."); end > 0 {
		candidate := strings.ToLower(trimmed[:end])
		if strings.HasPrefix(candidate, "q") {
			return candidate
		}
	}
	return fmt.Sprintf("section-%d", index)
}

func cleanBlock(lines []string) string {
	cleaned := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if trimmed == "" {
			if len(cleaned) != 0 && cleaned[len(cleaned)-1] != "" {
				cleaned = append(cleaned, "")
			}
			continue
		}
		if !inFence {
			trimmed = cleanMarkdown(trimmed)
		}
		cleaned = append(cleaned, trimmed)
	}
	for len(cleaned) > 0 && cleaned[len(cleaned)-1] == "" {
		cleaned = cleaned[:len(cleaned)-1]
	}
	return strings.Join(cleaned, "\n")
}

func cleanMarkdown(value string) string {
	value = strings.TrimSpace(value)
	value = markdownLink.ReplaceAllString(value, "$1")
	value = strings.ReplaceAll(value, "`", "")
	value = strings.ReplaceAll(value, "**", "")
	value = strings.ReplaceAll(value, "__", "")
	return strings.TrimSpace(value)
}
