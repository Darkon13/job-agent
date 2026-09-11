package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

func reviewAnswerTask(t *testing.T, payload core.ReviewAnswerPayload) core.Task {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return core.Task{ID: "review-answer-task", Type: core.TaskReviewAnswer, ProfileID: "primary", Payload: data, IdempotencyKey: "review-answer-1"}
}

func reviewFixture(t *testing.T, questions []core.Question, promptQuestionIndex int, block core.AnswerBlock) (*ReviewAnswerHandler, *storagememory.Repository, core.ReviewSession, core.ReviewPrompt, *recordingVacancyTestChain) {
	t.Helper()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	definition, err := core.NewProgressiveTestDefinition("hh", "vacancy:42", "Go basics", nil, now)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if _, err := repository.UpsertTestDefinition(context.Background(), definition); err != nil {
		t.Fatalf("store definition: %v", err)
	}
	questionnaire := core.Questionnaire{Title: "Go basics", Questions: questions}
	session, err := core.NewReviewSession("review-1", definition, "primary", "correlation-1", now)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	session.Questionnaire = questionnaire
	if _, err := repository.CreateReviewSession(context.Background(), session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	prompt := core.ReviewPrompt{
		ID: "review-1-prompt-1", SessionID: session.ID, Revision: session.Revision,
		Question: questions[promptQuestionIndex], CreatedAt: now,
	}
	if err := session.WaitForAnswer(prompt, now); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := repository.SaveReviewPrompt(context.Background(), session, prompt, session.Revision); err != nil {
		t.Fatalf("save prompt: %v", err)
	}
	chain := &recordingVacancyTestChain{}
	handler, err := NewReviewAnswerHandler(repository, fixedClock{now: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	handler.ConfigureContinuation(staticVacancyBlocks{"hh": block}, repository, chain)
	return handler, repository, session, prompt, chain
}

func TestReviewAnswerCompletesChainWithHumanAnswer(t *testing.T) {
	questions := []core.Question{
		{ID: "1", Text: "Explain experience", Kind: core.QuestionText},
		{ID: "2", Text: "Rate yourself", Kind: core.QuestionSingle, Options: []core.QuestionOption{
			{ID: "20", Text: "Junior"}, {ID: "21", Text: "Senior"},
		}},
	}
	block := core.AnswerBlock{
		Tag: "hh-vacancy", Name: "HH vacancy", Kind: core.AnswerBlockVacancy, Platform: "hh",
		Answers: []core.StoredAnswer{{Question: "Explain experience", Text: "Five years of Go"}},
	}
	handler, repository, _, prompt, chain := reviewFixture(t, questions, 1, block)
	err := handler.Handle(context.Background(), reviewAnswerTask(t, core.ReviewAnswerPayload{
		SessionID: "review-1", PromptID: prompt.ID, ExpectedRevision: 1,
		SelectedOptions: []string{"Senior"}, Source: "cli",
	}))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(chain.answers) != 1 || len(chain.answers[0]) != 2 {
		t.Fatalf("answers = %#v", chain.answers)
	}
	if chain.answers[0][0].Text != "Five years of Go" || chain.answers[0][1].SelectedOptionIDs[0] != "21" {
		t.Fatalf("resolved answers = %#v", chain.answers[0])
	}
	session, err := repository.ReviewSession(context.Background(), "review-1")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if session.Status != core.ReviewAnswered || session.Revision != 2 {
		t.Fatalf("session = %#v", session)
	}
	selections, err := repository.ReviewSelections(context.Background(), "review-1")
	if err != nil || len(selections) != 1 || selections[0].Source != "cli" {
		t.Fatalf("selections = %#v err=%v", selections, err)
	}
}

func TestReviewAnswerRequestsNextMissingQuestion(t *testing.T) {
	questions := []core.Question{
		{ID: "1", Text: "Explain experience", Kind: core.QuestionText},
		{ID: "2", Text: "Rate yourself", Kind: core.QuestionSingle, Options: []core.QuestionOption{
			{ID: "20", Text: "Junior"}, {ID: "21", Text: "Senior"},
		}},
		{ID: "3", Text: "Describe a project", Kind: core.QuestionText},
	}
	block := core.AnswerBlock{
		Tag: "hh-vacancy", Name: "HH vacancy", Kind: core.AnswerBlockVacancy, Platform: "hh",
		Answers: []core.StoredAnswer{{Question: "Explain experience", Text: "Five years of Go"}},
	}
	handler, repository, _, prompt, chain := reviewFixture(t, questions, 1, block)
	err := handler.Handle(context.Background(), reviewAnswerTask(t, core.ReviewAnswerPayload{
		SessionID: "review-1", PromptID: prompt.ID, ExpectedRevision: 1,
		SelectedOptions: []string{"Senior"}, Source: "cli",
	}))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(chain.answers) != 0 {
		t.Fatalf("answers = %#v", chain.answers)
	}
	session, err := repository.ReviewSession(context.Background(), "review-1")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if session.Status != core.ReviewWaiting || session.Revision != 2 {
		t.Fatalf("session = %#v", session)
	}
	next, err := repository.ReviewPrompt(context.Background(), core.ReviewPromptID("review-1-prompt-2"))
	if err != nil {
		t.Fatalf("load next prompt: %v", err)
	}
	if next.Question.ID != "3" {
		t.Fatalf("next prompt = %#v", next)
	}
}

func TestReviewAnswerAppendsReusableRevision(t *testing.T) {
	questions := []core.Question{
		{ID: "1", Text: "Explain experience", Kind: core.QuestionText},
		{ID: "2", Text: "Rate yourself", Kind: core.QuestionSingle, Options: []core.QuestionOption{
			{ID: "20", Text: "Junior"}, {ID: "21", Text: "Senior"},
		}},
	}
	block := core.AnswerBlock{
		Tag: "hh-vacancy", Name: "HH vacancy", Kind: core.AnswerBlockVacancy, Platform: "hh",
		Answers: []core.StoredAnswer{{Question: "Explain experience", Text: "Five years of Go"}},
	}
	handler, repository, _, prompt, _ := reviewFixture(t, questions, 1, block)
	err := handler.Handle(context.Background(), reviewAnswerTask(t, core.ReviewAnswerPayload{
		SessionID: "review-1", PromptID: prompt.ID, ExpectedRevision: 1,
		SelectedOptions: []string{"Senior"}, Source: "cli",
	}))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	latest, exists, err := repository.LatestAnswerBlockRevision(context.Background(), "hh-vacancy")
	if err != nil || !exists {
		t.Fatalf("latest revision: exists=%v err=%v", exists, err)
	}
	if latest.Revision != 1 || latest.Source != "review:review-1" || len(latest.Answers) != 2 {
		t.Fatalf("revision = %#v", latest)
	}
	if latest.Answers[1].Text != "" || latest.Answers[1].SelectedOptions[0] != "Senior" || latest.Answers[1].QuestionFingerprint == "" {
		t.Fatalf("human answer = %#v", latest.Answers[1])
	}
}

func TestReviewAnswerRejectsStaleRevision(t *testing.T) {
	questions := []core.Question{{ID: "1", Text: "Explain experience", Kind: core.QuestionText}}
	block := core.AnswerBlock{
		Tag: "hh-vacancy", Name: "HH vacancy", Kind: core.AnswerBlockVacancy, Platform: "hh",
		Answers: []core.StoredAnswer{{Question: "Explain experience", Text: "Five years of Go"}},
	}
	handler, _, _, prompt, chain := reviewFixture(t, questions, 0, block)
	err := handler.Handle(context.Background(), reviewAnswerTask(t, core.ReviewAnswerPayload{
		SessionID: "review-1", PromptID: prompt.ID, ExpectedRevision: 2,
		Text: "Five years of Go", Source: "cli",
	}))
	if err == nil {
		t.Fatal("expected stale revision error")
	}
	if len(chain.answers) != 0 {
		t.Fatalf("answers = %#v", chain.answers)
	}
}
