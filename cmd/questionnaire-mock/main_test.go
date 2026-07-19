package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuestionnaireEndpointExposesStablePerQuestionFingerprints(t *testing.T) {
	first := requestQuestionnaire(t, newMockHandler(1))
	second := requestQuestionnaire(t, newMockHandler(2))

	if first.AttemptFingerprint != second.AttemptFingerprint {
		t.Fatalf("attempt fingerprints differ: %s != %s", first.AttemptFingerprint, second.AttemptFingerprint)
	}
	if len(first.Questionnaire.Questions) != 2 {
		t.Fatalf("unexpected question count: %d", len(first.Questionnaire.Questions))
	}
	if first.Questionnaire.Questions[0].ID == second.Questionnaire.Questions[0].ID {
		t.Fatal("expected runtime IDs/order to change between mock sessions")
	}
	firstFingerprints := make(map[string]struct{}, len(first.QuestionFingerprints))
	for _, fingerprint := range first.QuestionFingerprints {
		firstFingerprints[fingerprint] = struct{}{}
	}
	for _, fingerprint := range second.QuestionFingerprints {
		if _, exists := firstFingerprints[fingerprint]; !exists {
			t.Fatalf("question fingerprint changed across shuffle: %s", fingerprint)
		}
	}
}

func TestIndexContainsAssessmentControls(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	newMockHandler(1).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status: %d", recorder.Code)
	}
	for _, expected := range []string{`name = 'answer'`, `data-qa="footer-next-button"`, `data-qa="progress"`, `answer-block-file`} {
		if !contains(recorder.Body.String(), expected) {
			t.Fatalf("response does not contain %q", expected)
		}
	}
}

func TestResolveEndpointMapsTextToCurrentRuntimeIDs(t *testing.T) {
	handler := newMockHandler(7)
	questionnaire := requestQuestionnaire(t, handler)
	answerBlock := map[string]any{
		"tag": "mock-reviewed", "name": "Synthetic block", "kind": "qualification", "platform": "mock",
		"match": map[string]any{},
		"answers": []map[string]any{
			answerForQuestion(questionnaire, "Вопрос 1: выберите один вариант", "Ответ В"),
			answerForQuestion(questionnaire, "Вопрос 2: выберите другой вариант", "Вариант Д"),
		},
	}
	body, err := json.Marshal(answerBlock)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/resolve", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", recorder.Code, recorder.Body.String())
	}
	var plan struct {
		Answers []struct {
			QuestionID        string   `json:"question_id"`
			SelectedOptionIDs []string `json:"selected_option_ids"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if len(plan.Answers) != 2 {
		t.Fatalf("unexpected answer count: %d", len(plan.Answers))
	}
	for _, answer := range plan.Answers {
		if answer.QuestionID == "" || len(answer.SelectedOptionIDs) != 1 || answer.SelectedOptionIDs[0] == "" {
			t.Fatalf("unresolved runtime IDs: %#v", answer)
		}
	}
}

func answerForQuestion(response questionnaireResponse, questionText, selectedOption string) map[string]any {
	for _, question := range response.Questionnaire.Questions {
		if question.Text == questionText {
			return map[string]any{
				"question": questionText, "question_fingerprint": response.QuestionFingerprints[question.ID],
				"selected_options": []string{selectedOption},
			}
		}
	}
	return nil
}

func requestQuestionnaire(t *testing.T, handler http.Handler) questionnaireResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/questionnaire", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", recorder.Code, recorder.Body.String())
	}
	var response questionnaireResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return response
}

func contains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
