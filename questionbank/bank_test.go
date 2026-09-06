package questionbank

import (
	"strings"
	"testing"

	"github.com/Darkon13/job-agent/core"
)

func TestParseMarkdownDistinguishesCompleteAndIncompleteQuestions(t *testing.T) {
	source := `## Docker — базовый уровень

#### Q1. Что запустить?

- [ ] docker build
- [x] docker run
- [ ] docker stop

#### Q2. Что такое образ?

- [x] Неизменяемый шаблон
`
	bank, report, err := ParseMarkdown(strings.NewReader(source), testMetadata())
	if err != nil {
		t.Fatal(err)
	}
	if report.Questions != 2 || report.CompleteQuestions != 1 || report.IncompleteQuestions != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if bank.Questions[0].Text != "Что запустить?" || !bank.Questions[0].OptionsComplete {
		t.Fatalf("unexpected complete question: %#v", bank.Questions[0])
	}
	if bank.Questions[0].SourceID != "q1" || bank.Questions[1].SourceID != "q2" {
		t.Fatalf("unexpected source ids: %#v", bank.Questions)
	}
	if bank.Questions[1].OptionsComplete {
		t.Fatalf("answer-only question must remain incomplete: %#v", bank.Questions[1])
	}
}

func TestBuildPracticeFixtureUsesOnlyCompleteQuestions(t *testing.T) {
	source := `## Docker — базовый уровень

#### Q1. Что запустить?

- [ ] docker build
- [x] docker run
- [ ] docker stop

#### Q2. Что такое образ?

- [x] Неизменяемый шаблон
`
	bank, _, err := ParseMarkdown(strings.NewReader(source), testMetadata())
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := BuildPracticeFixture(bank)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.Skipped != 1 || len(fixture.Questionnaire.Questions) != 1 {
		t.Fatalf("unexpected fixture: %#v", fixture)
	}
	if fixture.AnswerBlock.Platform != "study" || fixture.AnswerBlock.Answers[0].QuestionFingerprint == "" {
		t.Fatalf("unsafe or incomplete answer block: %#v", fixture.AnswerBlock)
	}
	if _, err := core.ResolveAnswerBlock(fixture.Questionnaire, fixture.AnswerBlock); err != nil {
		t.Fatalf("resolve imported fixture: %v", err)
	}
}

func TestParseMarkdownUsesCodeFollowingArrowAsOptionText(t *testing.T) {
	source := strings.Join([]string{
		"## Python — средний уровень",
		"",
		"#### Q1. Какой фрагмент верен?",
		"- [ ] ↓",
		"```",
		"broken()",
		"```",
		"- [x] ↓",
		"```",
		"working()",
		"```",
	}, "\n")
	bank, report, err := ParseMarkdown(strings.NewReader(source), testMetadata())
	if err != nil {
		t.Fatal(err)
	}
	if report.CompleteQuestions != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if got := bank.Questions[0].SuggestedOptions[0]; got != "working()" {
		t.Fatalf("unexpected suggested code: %q", got)
	}
}

func testMetadata() ImportMetadata {
	return ImportMetadata{
		Tag: "hh-community-docker-basic", Platform: "study", TargetPlatform: "hh",
		Qualification: core.QualificationDescriptor{
			FamilyID: "community:docker", FamilyName: "Docker",
			LevelID: "community:basic", LevelName: "Базовый",
		},
		Source: Source{
			Repository: "https://example.test/quizzes", Revision: "abc123",
			Path: "docker/basic.md", License: "AGPL-3.0-only",
		},
	}
}
