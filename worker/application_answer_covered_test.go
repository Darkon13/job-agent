package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

func TestApplicationAnswerCoveredResumesFullyCoveredQuestionnaire(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	definition, err := core.NewProgressiveTestDefinition("hh", "vacancy:777", "Go basics", nil, now)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if _, err := repository.UpsertTestDefinition(ctx, definition); err != nil {
		t.Fatalf("store definition: %v", err)
	}
	questions := []core.Question{
		{ID: "1", Text: "Pick a language", Kind: core.QuestionSingle, Options: []core.QuestionOption{
			{ID: "10", Text: "Go"}, {ID: "11", Text: "Python"},
		}},
		{ID: "2", Text: "Explain experience", Kind: core.QuestionText},
	}
	questionnaire := core.Questionnaire{Title: "Go basics", Questions: questions}
	session, err := core.NewReviewSession("review-1", definition, "primary", "correlation-1", now)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	session.Questionnaire = questionnaire
	if _, err := repository.CreateReviewSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	prompt := core.ReviewPrompt{
		ID: "review-1-prompt-1", SessionID: session.ID, Revision: session.Revision,
		Question: questions[0], CreatedAt: now,
	}
	if err := session.WaitForAnswer(prompt, now); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := repository.SaveReviewPrompt(ctx, session, prompt, session.Revision); err != nil {
		t.Fatalf("save prompt: %v", err)
	}

	block := core.AnswerBlock{
		Tag: "hh-vacancy-reviewed", Name: "Vacancy questionnaire", Kind: core.AnswerBlockVacancy, Platform: "hh",
		Answers: []core.StoredAnswer{
			{Question: "Pick a language", SelectedOptions: []string{"Go"}},
			{Question: "Explain experience", Text: "Five years of Go"},
		},
	}
	chain := &recordingVacancyTestChain{}
	handler, err := NewApplicationAnswerCoveredHandler(repository, staticVacancyBlocks{"hh": block}, chain, fixedClock{now: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationAnswerCoveredPayload{ProfileID: "primary", Limit: 5})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "answer-covered-1", Type: core.TaskApplicationAnswerCovered, IdempotencyKey: "answer-covered-run-1",
		Source: "test", Platform: "hh", ProfileID: "primary", CorrelationID: "correlation-covered", Payload: payload,
	}, now)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if err := handler.Handle(ctx, task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(chain.answers) != 1 || len(chain.answers[0]) != 2 {
		t.Fatalf("enqueued answers = %#v", chain.answers)
	}
	if chain.answers[0][0].SelectedOptionIDs[0] != "10" || chain.answers[0][1].Text != "Five years of Go" {
		t.Fatalf("resolved answers = %#v", chain.answers[0])
	}
}

func TestApplicationAnswerCoveredSkipsPartiallyCoveredQuestionnaire(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	definition, err := core.NewProgressiveTestDefinition("hh", "vacancy:778", "Go basics", nil, now)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if _, err := repository.UpsertTestDefinition(ctx, definition); err != nil {
		t.Fatalf("store definition: %v", err)
	}
	questionnaire := core.Questionnaire{Questions: []core.Question{
		{ID: "1", Text: "Pick a language", Kind: core.QuestionSingle, Options: []core.QuestionOption{{ID: "10", Text: "Go"}}},
		{ID: "2", Text: "Explain experience", Kind: core.QuestionText},
	}}
	session, err := core.NewReviewSession("review-2", definition, "primary", "correlation-2", now)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	session.Questionnaire = questionnaire
	if _, err := repository.CreateReviewSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	block := core.AnswerBlock{
		Tag: "hh-vacancy-reviewed", Name: "Vacancy questionnaire", Kind: core.AnswerBlockVacancy, Platform: "hh",
		Answers: []core.StoredAnswer{{Question: "Pick a language", SelectedOptions: []string{"Go"}}},
	}
	chain := &recordingVacancyTestChain{}
	handler, err := NewApplicationAnswerCoveredHandler(repository, staticVacancyBlocks{"hh": block}, chain, fixedClock{now: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationAnswerCoveredPayload{ProfileID: "primary"})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "answer-covered-2", Type: core.TaskApplicationAnswerCovered, IdempotencyKey: "answer-covered-run-2",
		Source: "test", Platform: "hh", ProfileID: "primary", CorrelationID: "correlation-covered-2", Payload: payload,
	}, now)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if err := handler.Handle(ctx, task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(chain.answers) != 0 {
		t.Fatalf("partially covered questionnaire was submitted: %#v", chain.answers)
	}
}
