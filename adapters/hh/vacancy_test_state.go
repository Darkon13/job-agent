package hh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"

	"github.com/Darkon13/job-agent/core"
)

var vacancyTestStateMarkers = []string{
	"HH-Lux-InitialState",
	"VacancyResponsePopup-InitialState",
	"VacancyResponse-InitialState",
}

type vacancyResponseState struct {
	VacancyTests         map[string]vacancyTest `json:"vacancyTests"`
	VacancyResponsePopup struct {
		Vacancy struct {
			Test struct {
				HasTests bool       `json:"hasTests"`
				TestID   flexibleID `json:"testId"`
				Required bool       `json:"required"`
			} `json:"test"`
		} `json:"vacancy"`
	} `json:"vacancyResponsePopup"`
}

type vacancyTest struct {
	UIDPk       flexibleID      `json:"uidPk"`
	GUID        string          `json:"guid"`
	Description string          `json:"description"`
	Required    flexibleBool    `json:"required"`
	StartTime   json.RawMessage `json:"startTime"`
	Tasks       []vacancyTask   `json:"tasks"`
}

type vacancyTask struct {
	ID                 flexibleID            `json:"id"`
	Description        string                `json:"description"`
	Multiple           flexibleBool          `json:"multiple"`
	Open               flexibleBool          `json:"open"`
	CandidateSolutions []vacancyTaskSolution `json:"candidateSolutions"`
}

// flexibleBool accepts native JSON booleans and the string booleans HH embeds
// in the rendered popup state ("multiple": "true").
type flexibleBool bool

func (value *flexibleBool) UnmarshalJSON(data []byte) error {
	var flag bool
	if err := json.Unmarshal(data, &flag); err == nil {
		*value = flexibleBool(flag)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "true", "1":
			*value = true
		case "false", "0", "":
			*value = false
		default:
			return fmt.Errorf("boolean field has unsupported value %q", text)
		}
		return nil
	}
	return errors.New("boolean field must be a boolean or a string")
}

type vacancyTaskSolution struct {
	ID          flexibleID `json:"id"`
	Description string     `json:"description"`
	Text        string     `json:"text"`
}

// ParseVacancyTestQuestionnaire extracts the questionnaire of the vacancy
// response popup. Short-lived submission context (xsrf, guid, startTime) stays
// inside the adapter and is never persisted in the questionnaire.
func ParseVacancyTestQuestionnaire(document []byte) (core.Questionnaire, error) {
	_, test, err := parseVacancyTestState(document)
	if err != nil {
		return core.Questionnaire{}, err
	}
	return vacancyTestQuestionnaire(test)
}

func vacancyTestQuestionnaire(test vacancyTest) (core.Questionnaire, error) {
	questionnaire := core.Questionnaire{Title: strings.TrimSpace(test.Description)}
	for _, task := range test.Tasks {
		question, err := vacancyTaskQuestion(task)
		if err != nil {
			return core.Questionnaire{}, err
		}
		questionnaire.Questions = append(questionnaire.Questions, question)
	}
	if len(questionnaire.Questions) == 0 {
		return core.Questionnaire{}, errors.New("HH vacancy test contains no tasks")
	}
	for _, question := range questionnaire.Questions {
		if _, err := core.QuestionFingerprint(question); err != nil {
			return core.Questionnaire{}, fmt.Errorf("HH vacancy test question is invalid: %w", err)
		}
	}
	return questionnaire, nil
}

func parseVacancyTestState(document []byte) (vacancyResponseState, vacancyTest, error) {
	raw, err := extractInitialStateByMarkers(document, vacancyTestStateMarkers...)
	if err != nil {
		return vacancyResponseState{}, vacancyTest{}, err
	}
	var state vacancyResponseState
	if err := json.Unmarshal([]byte(html.UnescapeString(string(raw))), &state); err != nil {
		return vacancyResponseState{}, vacancyTest{}, fmt.Errorf("decode HH vacancy test state: %w", err)
	}
	test, ok := selectVacancyTest(state)
	if !ok {
		return vacancyResponseState{}, vacancyTest{}, errors.New("HH vacancy response does not contain a test")
	}
	return state, test, nil
}

func selectVacancyTest(state vacancyResponseState) (vacancyTest, bool) {
	if testID := strings.TrimSpace(string(state.VacancyResponsePopup.Vacancy.Test.TestID)); testID != "" {
		if test, exists := state.VacancyTests[testID]; exists {
			return test, true
		}
	}
	for _, test := range state.VacancyTests {
		return test, true
	}
	return vacancyTest{}, false
}

func vacancyTaskQuestion(task vacancyTask) (core.Question, error) {
	id := strings.TrimSpace(string(task.ID))
	text := strings.TrimSpace(task.Description)
	if id == "" || text == "" {
		return core.Question{}, errors.New("HH vacancy task requires id and description")
	}
	question := core.Question{ID: id, Text: text}
	switch {
	case bool(task.Open):
		// An open task is rendered as a textarea. When it also lists candidate
		// solutions HH adds an "open" branch ("Свой вариант"), so the core
		// question stays open text and the browser submitter selects that
		// branch together with the submitted text.
		question.Kind = core.QuestionText
	case len(task.CandidateSolutions) > 0:
		question.Kind = core.QuestionSingle
		if bool(task.Multiple) {
			question.Kind = core.QuestionMultiple
		}
		for _, solution := range task.CandidateSolutions {
			optionID := strings.TrimSpace(string(solution.ID))
			optionText := strings.TrimSpace(solution.Text)
			if optionText == "" {
				optionText = strings.TrimSpace(solution.Description)
			}
			if optionID == "" || optionText == "" {
				return core.Question{}, fmt.Errorf("HH vacancy task %q contains an invalid option", text)
			}
			question.Options = append(question.Options, core.QuestionOption{ID: optionID, Text: optionText})
		}
	default:
		question.Kind = core.QuestionCode
	}
	return question, nil
}

func extractInitialStateByMarkers(document []byte, markers ...string) ([]byte, error) {
	content := string(document)
	for _, marker := range markers {
		pattern := regexp.MustCompile(`(?s)<template[^>]*` + regexp.QuoteMeta(marker) + `[^>]*>(.*?)</template>`)
		if match := pattern.FindStringSubmatch(content); match != nil {
			return []byte(match[1]), nil
		}
	}
	return nil, errors.New("HH page does not contain the expected initial state")
}

// CaptureVacancyTest reads the vacancy response popup through the logged-in
// browser session and returns the normalized questionnaire.
func (client *BrowserReadClient) CaptureVacancyTest(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) (core.Questionnaire, error) {
	if profileID == "" || profileID != client.profileID {
		return core.Questionnaire{}, errors.New("HH browser vacancy reader profile does not match")
	}
	if key.Platform != Name {
		return core.Questionnaire{}, errors.New("HH test capture requires an HH vacancy")
	}
	endpoint := strings.TrimRight(client.webBaseURL, "/") + "/applicant/vacancy_response?" + url.Values{
		"vacancyId":           []string{key.ExternalID},
		"startedWithQuestion": []string{"true"},
	}.Encode()
	document, finalURL, err := client.getHTML(ctx, endpoint, "vacancies.test.capture")
	if err != nil {
		return core.Questionnaire{}, err
	}
	if isLoginURL(finalURL) {
		return core.Questionnaire{}, operationError(core.ErrorUnauthorized, "vacancies.test.capture", "HH browser session requires authentication", nil)
	}
	rendered, err := renderHTMLNode(document)
	if err != nil {
		return core.Questionnaire{}, operationError(core.ErrorTemporaryFailure, "vacancies.test.capture", "HH popup could not be rendered", err)
	}
	return ParseVacancyTestQuestionnaire(rendered)
}

func renderHTMLNode(node *html.Node) ([]byte, error) {
	var builder strings.Builder
	if err := html.Render(&builder, node); err != nil {
		return nil, err
	}
	return []byte(builder.String()), nil
}
