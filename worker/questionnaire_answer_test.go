package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

type stubVacancyTestSubmitter struct {
	calls   []core.VacancyKey
	answers [][]core.ResolvedAnswer
	err     error
}

func (stub *stubVacancyTestSubmitter) SubmitVacancyTest(_ context.Context, _ core.ProfileID, key core.VacancyKey, answers []core.ResolvedAnswer) error {
	stub.calls = append(stub.calls, key)
	stub.answers = append(stub.answers, answers)
	return stub.err
}

func questionnaireAnswerTask(t *testing.T, payload core.QuestionnaireAnswerPayload) core.Task {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return core.Task{Type: core.TaskQuestionnaireAnswer, ProfileID: payload.ProfileID, Payload: data, IdempotencyKey: "questionnaire.answer:test"}
}

func TestQuestionnaireAnswerSubmitsResolvedAnswers(t *testing.T) {
	submitter := &stubVacancyTestSubmitter{}
	submitters := NewVacancyTestSubmitterRegistry()
	if err := submitters.Register("primary", submitter); err != nil {
		t.Fatalf("register: %v", err)
	}
	handler, err := NewQuestionnaireAnswerHandler(submitters)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	payload := core.QuestionnaireAnswerPayload{
		ProfileID: "primary", Platform: "hh", VacancyExternalID: "42",
		Answers: []core.ResolvedAnswer{{QuestionID: "1", Text: "Five years of Go"}},
	}
	if err := handler.Handle(context.Background(), questionnaireAnswerTask(t, payload)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(submitter.calls) != 1 || submitter.calls[0].ExternalID != "42" {
		t.Fatalf("calls = %#v", submitter.calls)
	}
	if len(submitter.answers) != 1 || submitter.answers[0][0].Text != "Five years of Go" {
		t.Fatalf("answers = %#v", submitter.answers)
	}
}

func TestQuestionnaireAnswerRejectsInvalidPayload(t *testing.T) {
	submitter := &stubVacancyTestSubmitter{}
	submitters := NewVacancyTestSubmitterRegistry()
	if err := submitters.Register("primary", submitter); err != nil {
		t.Fatalf("register: %v", err)
	}
	handler, err := NewQuestionnaireAnswerHandler(submitters)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	cases := []core.QuestionnaireAnswerPayload{
		{ProfileID: "primary", Platform: "hh", VacancyExternalID: "42"},
		{ProfileID: "primary", Platform: "hh", VacancyExternalID: "42", Answers: []core.ResolvedAnswer{{}}},
		{ProfileID: "primary", Platform: "hh", VacancyExternalID: "42", Answers: []core.ResolvedAnswer{
			{QuestionID: "1", Text: "answer", SelectedOptionIDs: []string{"10"}},
		}},
	}
	for _, payload := range cases {
		if err := handler.Handle(context.Background(), questionnaireAnswerTask(t, payload)); err == nil {
			t.Fatalf("payload %#v unexpectedly passed", payload)
		}
	}
	if len(submitter.calls) != 0 {
		t.Fatalf("submitted with invalid payload: %#v", submitter.calls)
	}
}

func TestQuestionnaireAnswerPropagatesAdapterError(t *testing.T) {
	expected := &core.OperationError{Category: core.ErrorValidationRequired, Operation: "vacancies.test.submit", Message: "HH did not confirm the vacancy test submission"}
	submitter := &stubVacancyTestSubmitter{err: expected}
	submitters := NewVacancyTestSubmitterRegistry()
	if err := submitters.Register("primary", submitter); err != nil {
		t.Fatalf("register: %v", err)
	}
	handler, err := NewQuestionnaireAnswerHandler(submitters)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	err = handler.Handle(context.Background(), questionnaireAnswerTask(t, core.QuestionnaireAnswerPayload{
		ProfileID: "primary", Platform: "hh", VacancyExternalID: "42",
		Answers: []core.ResolvedAnswer{{QuestionID: "1", Text: "answer"}},
	}))
	if !errors.Is(err, expected) {
		t.Fatalf("error = %v", err)
	}
}

var _ adapter.VacancyTestSubmitter = (*stubVacancyTestSubmitter)(nil)
