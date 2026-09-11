package worker

import (
	"context"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type staticVacancyBlocks map[core.Platform]core.AnswerBlock

func (blocks staticVacancyBlocks) FindVacancy(_ context.Context, platform core.Platform) (core.AnswerBlock, bool, error) {
	block, exists := blocks[platform]
	return block, exists, nil
}

type recordingVacancyTestChain struct {
	answers   [][]core.ResolvedAnswer
	completes []core.TestAttemptStatus
}

func (chain *recordingVacancyTestChain) EnqueueCapture(context.Context, core.ProfileID, core.Platform, string, string) (bool, error) {
	return true, nil
}

func (chain *recordingVacancyTestChain) EnqueueAnswer(_ context.Context, _ core.ProfileID, _ core.Platform, _ string, answers []core.ResolvedAnswer, _ string) (bool, error) {
	chain.answers = append(chain.answers, answers)
	return true, nil
}

func (chain *recordingVacancyTestChain) EnqueueComplete(_ context.Context, _ core.ProfileID, _ core.Platform, _ string, status core.TestAttemptStatus, _, _ string) (bool, error) {
	chain.completes = append(chain.completes, status)
	return true, nil
}

func vacancyTestBlock() core.AnswerBlock {
	return core.AnswerBlock{
		Tag: "hh-vacancy", Name: "HH vacancy", Kind: core.AnswerBlockVacancy, Platform: "hh",
		Answers: []core.StoredAnswer{
			{Question: "Explain experience", Text: "Five years of Go"},
			{Question: "Rate yourself", SelectedOptions: []string{"Senior"}},
		},
	}
}

func vacancyTestQuestions() core.Questionnaire {
	return core.Questionnaire{
		Title: "Go basics",
		Questions: []core.Question{
			{ID: "1", Text: "Explain experience", Kind: core.QuestionText},
			{ID: "2", Text: "Rate yourself", Kind: core.QuestionSingle, Options: []core.QuestionOption{
				{ID: "20", Text: "Junior"}, {ID: "21", Text: "Senior"},
			}},
		},
	}
}

func TestVacancyTestCaptureEnqueuesAnswersOnFullCoverage(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	capturer := &stubVacancyTestCapturer{questionnaire: vacancyTestQuestions()}
	capturers := NewVacancyTestCapturerRegistry()
	if err := capturers.Register("primary", capturer); err != nil {
		t.Fatalf("register: %v", err)
	}
	repository := storagememory.NewRepository()
	handler, err := NewVacancyTestCaptureHandler(capturers, repository, fixedClock{now: now})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	chain := &recordingVacancyTestChain{}
	handler.ConfigureAnswerRouting(staticVacancyBlocks{"hh": vacancyTestBlock()}, repository, chain)
	if err := handler.Handle(context.Background(), testCaptureTask(t, "primary", "hh", "42")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(chain.answers) != 1 || len(chain.answers[0]) != 2 {
		t.Fatalf("answers = %#v", chain.answers)
	}
	if chain.answers[0][1].SelectedOptionIDs[0] != "21" {
		t.Fatalf("resolved choice = %#v", chain.answers[0][1])
	}
	definitions, err := repository.ListTestDefinitions(context.Background(), storage.TestDefinitionFilter{Platform: "hh"})
	if err != nil || len(definitions) != 1 {
		t.Fatalf("definitions = %#v err=%v", definitions, err)
	}
	sessionID, _ := vacancyReviewIDs(definitions[0], "primary")
	if _, err := repository.ReviewSession(context.Background(), sessionID); err == nil {
		t.Fatal("unexpected review session for full coverage")
	}
}

func TestVacancyTestCaptureCreatesReviewSessionForMissingAnswers(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	capturer := &stubVacancyTestCapturer{questionnaire: vacancyTestQuestions()}
	capturers := NewVacancyTestCapturerRegistry()
	if err := capturers.Register("primary", capturer); err != nil {
		t.Fatalf("register: %v", err)
	}
	repository := storagememory.NewRepository()
	handler, err := NewVacancyTestCaptureHandler(capturers, repository, fixedClock{now: now})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	partial := vacancyTestBlock()
	partial.Answers = partial.Answers[:1]
	chain := &recordingVacancyTestChain{}
	handler.ConfigureAnswerRouting(staticVacancyBlocks{"hh": partial}, repository, chain)
	if err := handler.Handle(context.Background(), testCaptureTask(t, "primary", "hh", "42")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(chain.answers) != 0 {
		t.Fatalf("answers = %#v", chain.answers)
	}
	definitions, err := repository.ListTestDefinitions(context.Background(), storage.TestDefinitionFilter{Platform: "hh"})
	if err != nil || len(definitions) != 1 {
		t.Fatalf("definitions = %#v err=%v", definitions, err)
	}
	sessionID, promptID := vacancyReviewIDs(definitions[0], "primary")
	session, err := repository.ReviewSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if session.Status != core.ReviewWaiting {
		t.Fatalf("session = %#v", session)
	}
	prompt, err := repository.ReviewPrompt(context.Background(), promptID)
	if err != nil {
		t.Fatalf("load prompt: %v", err)
	}
	if prompt.Question.ID != "2" || prompt.Question.Text != "Rate yourself" {
		t.Fatalf("prompt = %#v", prompt)
	}
}

func TestVacancyTestCaptureWithoutBlockStaysCatalogOnly(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	capturer := &stubVacancyTestCapturer{questionnaire: vacancyTestQuestions()}
	capturers := NewVacancyTestCapturerRegistry()
	if err := capturers.Register("primary", capturer); err != nil {
		t.Fatalf("register: %v", err)
	}
	repository := storagememory.NewRepository()
	handler, err := NewVacancyTestCaptureHandler(capturers, repository, fixedClock{now: now})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	chain := &recordingVacancyTestChain{}
	handler.ConfigureAnswerRouting(staticVacancyBlocks{}, repository, chain)
	if err := handler.Handle(context.Background(), testCaptureTask(t, "primary", "hh", "42")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(chain.answers) != 0 || len(chain.completes) != 0 {
		t.Fatalf("chain = %#v", chain)
	}
}
