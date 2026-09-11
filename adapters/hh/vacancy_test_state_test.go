package hh

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/Darkon13/job-agent/core"
)

func vacancyTestPage(state string) string {
	return `<html><body><template data-name="VacancyResponsePopup-InitialState">` + state + `</template></body></html>`
}

func TestParseVacancyTestQuestionnaireMapsTasks(t *testing.T) {
	state := `{
		"vacancyResponsePopup":{"vacancy":{"test":{"hasTests":true,"testId":"10","required":true}}},
		"vacancyTests":{"10":{"uidPk":123,"guid":"guid-1","description":"Go basics","required":true,"startTime":1750000000,
			"tasks":[
				{"id":"1","description":"Pick a language","candidateSolutions":[{"id":10,"description":"Go"},{"id":11,"description":"Python"}]},
				{"id":"2","description":"Explain experience","open":true},
				{"id":"3","description":"Pick frameworks","multiple":true,"candidateSolutions":[{"id":20,"description":"Gin"},{"id":21,"description":"Echo"}]},
				{"id":"4","description":"Rate yourself","candidateSolutions":[]}
			]}}
	}`
	questionnaire, err := ParseVacancyTestQuestionnaire([]byte(vacancyTestPage(state)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if questionnaire.Title != "Go basics" || len(questionnaire.Questions) != 4 {
		t.Fatalf("questionnaire = %#v", questionnaire)
	}
	if questionnaire.Questions[0].Kind != core.QuestionSingle || len(questionnaire.Questions[0].Options) != 2 {
		t.Fatalf("question 0 = %#v", questionnaire.Questions[0])
	}
	if questionnaire.Questions[1].Kind != core.QuestionText || len(questionnaire.Questions[1].Options) != 0 {
		t.Fatalf("question 1 = %#v", questionnaire.Questions[1])
	}
	if questionnaire.Questions[2].Kind != core.QuestionMultiple {
		t.Fatalf("question 2 = %#v", questionnaire.Questions[2])
	}
	if questionnaire.Questions[3].Kind != core.QuestionCode {
		t.Fatalf("question 3 = %#v", questionnaire.Questions[3])
	}
	if _, err := core.QuestionFingerprint(questionnaire.Questions[0]); err != nil {
		t.Fatalf("question fingerprint: %v", err)
	}
}

func TestParseVacancyTestQuestionnaireRejectsUnknownPage(t *testing.T) {
	if _, err := ParseVacancyTestQuestionnaire([]byte(`<html><body>no state</body></html>`)); err == nil {
		t.Fatal("expected missing initial state to fail")
	}
	state := `{"vacancyResponsePopup":{"vacancy":{"test":{"hasTests":false}}},"vacancyTests":{}}`
	if _, err := ParseVacancyTestQuestionnaire([]byte(vacancyTestPage(state))); err == nil {
		t.Fatal("expected missing test to fail")
	}
	state = `{"vacancyResponsePopup":{"vacancy":{"test":{"hasTests":true,"testId":"10"}}},"vacancyTests":{"10":{"uidPk":1,"guid":"g","tasks":[{"id":"1","description":"Broken","candidateSolutions":[{"id":10,"description":""}]}]}}}`
	if _, err := ParseVacancyTestQuestionnaire([]byte(vacancyTestPage(state))); err == nil {
		t.Fatal("expected invalid option to fail")
	}
}

func TestBrowserCaptureVacancyTest(t *testing.T) {
	state := `{"vacancyResponsePopup":{"vacancy":{"test":{"hasTests":true,"testId":"10"}}},
		"vacancyTests":{"10":{"uidPk":1,"guid":"g","description":"Go basics","tasks":[
			{"id":"1","description":"Pick one","candidateSolutions":[{"id":10,"description":"Go"}]}]}}}`
	client := newBrowserReadClientFixture(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/applicant/vacancy_response" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("vacancyId") != "42" || request.URL.Query().Get("startedWithQuestion") != "false" {
			t.Errorf("query = %v", request.URL.Query())
		}
		_, _ = fmt.Fprint(response, vacancyTestPage(state))
	}))
	questionnaire, err := client.CaptureVacancyTest(context.Background(), "primary", core.VacancyKey{Platform: Name, ExternalID: "42"})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if questionnaire.Title != "Go basics" || len(questionnaire.Questions) != 1 {
		t.Fatalf("questionnaire = %#v", questionnaire)
	}
}
