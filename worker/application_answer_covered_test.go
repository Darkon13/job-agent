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

type fakeQuestionnaireAnswerModel struct {
	answers map[string]core.StoredAnswer
}

func (model fakeQuestionnaireAnswerModel) Tag() string { return "test-model" }

func (model fakeQuestionnaireAnswerModel) Resolve(_ context.Context, _ core.Platform, question core.Question) (core.StoredAnswer, error) {
	answer, ok := model.answers[core.NormalizeQuestionText(question.Text)]
	if !ok {
		return core.StoredAnswer{}, context.Canceled
	}
	answer.Question = question.Text
	return answer, nil
}

func TestApplicationAnswerCoveredUsesModelForMissingQuestion(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	definition, err := core.NewProgressiveTestDefinition("hh", "vacancy:779", "Go basics", nil, now)
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
	session, err := core.NewReviewSession("review-3", definition, "primary", "correlation-3", now)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	session.Questionnaire = questionnaire
	if _, err := repository.CreateReviewSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	prompt := core.ReviewPrompt{
		ID: "review-3-prompt-1", SessionID: session.ID, Revision: session.Revision,
		Question: questionnaire.Questions[0], CreatedAt: now,
	}
	if err := session.WaitForAnswer(prompt, now); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := repository.SaveReviewPrompt(ctx, session, prompt, session.Revision); err != nil {
		t.Fatalf("save prompt: %v", err)
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
	handler.ConfigureAnswerModels(map[core.ProfileID]QualificationAnswerModel{
		"primary": fakeQuestionnaireAnswerModel{answers: map[string]core.StoredAnswer{
			"explain experience": {Text: "Three years of Go"},
		}},
	})
	payload, _ := json.Marshal(core.ApplicationAnswerCoveredPayload{ProfileID: "primary"})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "answer-covered-3", Type: core.TaskApplicationAnswerCovered, IdempotencyKey: "answer-covered-run-3",
		Source: "test", Platform: "hh", ProfileID: "primary", CorrelationID: "correlation-covered-3", Payload: payload,
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
	if chain.answers[0][1].Text != "Three years of Go" {
		t.Fatalf("model answer = %#v", chain.answers[0][1])
	}
}

func TestApplicationAnswerCoveredSkipsQualificationSessionsWhenLimited(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	qualificationDefinition, err := core.NewProgressiveTestDefinition("hh", "skill:family:level", "Qualification", nil, now)
	if err != nil {
		t.Fatalf("qualification definition: %v", err)
	}
	if _, err := repository.UpsertTestDefinition(ctx, qualificationDefinition); err != nil {
		t.Fatalf("store qualification definition: %v", err)
	}
	qualification, err := core.NewReviewSession("review-qualification", qualificationDefinition, "primary", "correlation-q", now)
	if err != nil {
		t.Fatalf("qualification session: %v", err)
	}
	qualification.AnswerBlockTag = "hh-go-basic"
	qualification.Questionnaire = core.Questionnaire{Questions: []core.Question{{ID: "q1", Text: "Question", Kind: core.QuestionText}}}
	if _, err := repository.CreateReviewSession(ctx, qualification); err != nil {
		t.Fatalf("create qualification session: %v", err)
	}
	prompt := core.ReviewPrompt{ID: "qualification-prompt", SessionID: qualification.ID, Revision: qualification.Revision, Question: qualification.Questionnaire.Questions[0], CreatedAt: now.Add(time.Second)}
	if err := qualification.WaitForAnswer(prompt, now.Add(time.Second)); err != nil {
		t.Fatalf("wait qualification: %v", err)
	}
	if err := repository.SaveReviewPrompt(ctx, qualification, prompt, qualification.Revision); err != nil {
		t.Fatalf("save qualification prompt: %v", err)
	}

	vacancyDefinition, err := core.NewProgressiveTestDefinition("hh", "vacancy:900", "Vacancy", nil, now)
	if err != nil {
		t.Fatalf("vacancy definition: %v", err)
	}
	if _, err := repository.UpsertTestDefinition(ctx, vacancyDefinition); err != nil {
		t.Fatalf("store vacancy definition: %v", err)
	}
	vacancy, err := core.NewReviewSession("review-vacancy", vacancyDefinition, "primary", "correlation-v", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("vacancy session: %v", err)
	}
	vacancy.Questionnaire = core.Questionnaire{Questions: []core.Question{{ID: "v1", Text: "Vacancy question", Kind: core.QuestionText}}}
	if _, err := repository.CreateReviewSession(ctx, vacancy); err != nil {
		t.Fatalf("create vacancy session: %v", err)
	}
	vacancyPrompt := core.ReviewPrompt{ID: "vacancy-prompt", SessionID: vacancy.ID, Revision: vacancy.Revision, Question: vacancy.Questionnaire.Questions[0], CreatedAt: now.Add(3 * time.Second)}
	if err := vacancy.WaitForAnswer(vacancyPrompt, now.Add(3*time.Second)); err != nil {
		t.Fatalf("wait vacancy: %v", err)
	}
	if err := repository.SaveReviewPrompt(ctx, vacancy, vacancyPrompt, vacancy.Revision); err != nil {
		t.Fatalf("save vacancy prompt: %v", err)
	}

	chain := &recordingVacancyTestChain{}
	handler, err := NewApplicationAnswerCoveredHandler(repository, staticVacancyBlocks{}, chain, fixedClock{now: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	handler.ConfigureAnswerModels(map[core.ProfileID]QualificationAnswerModel{
		"primary": fakeQuestionnaireAnswerModel{answers: map[string]core.StoredAnswer{"vacancy question": {Text: "Answer"}}},
	})
	payload, _ := json.Marshal(core.ApplicationAnswerCoveredPayload{ProfileID: "primary", Limit: 1})
	task, err := core.NewTask(core.NewTaskParams{
		ID: "answer-covered-limit", Type: core.TaskApplicationAnswerCovered, IdempotencyKey: "answer-covered-limit",
		Source: "test", Platform: "hh", ProfileID: "primary", CorrelationID: "correlation-limit", Payload: payload,
	}, now)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if err := handler.Handle(ctx, task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(chain.answers) != 1 || len(chain.answers[0]) != 1 {
		t.Fatalf("vacancy session was hidden by qualification page limit: %#v", chain.answers)
	}
}
