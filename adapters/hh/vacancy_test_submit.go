package hh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/Darkon13/job-agent/core"
)

const vacancyTestSubmitOperation = "vacancies.test.submit"

// vacancyTestCapture couples the normalized questionnaire with the
// short-lived popup form context. It never leaves the adapter.
type vacancyTestCapture struct {
	Questionnaire    core.Questionnaire
	ActionURL        string
	Fields           map[string]string
	Tasks            []vacancyTestFormTask
	ResumeVisibility map[string]browserResumeVisibility
}

// vacancyTestFormTask mirrors one HH task as a form field contract. Open tasks
// use the task textarea; choice tasks use one form value per selected option.
type vacancyTestFormTask struct {
	ID        string
	Open      bool
	Multiple  bool
	OptionIDs map[string]struct{}
}

func vacancyResponsePageURL(baseURL, externalID string) string {
	return strings.TrimRight(baseURL, "/") + "/applicant/vacancy_response?" + url.Values{
		"vacancyId":           []string{externalID},
		"startedWithQuestion": []string{"true"},
	}.Encode()
}

func parseVacancyTestCapture(document []byte, actionURL string) (vacancyTestCapture, error) {
	state, test, err := parseVacancyTestState(document)
	if err != nil {
		return vacancyTestCapture{}, err
	}
	questionnaire, err := vacancyTestQuestionnaire(test)
	if err != nil {
		return vacancyTestCapture{}, err
	}
	xsrf := htmlInputValue(document, "_xsrf")
	if xsrf == "" {
		return vacancyTestCapture{}, operationError(core.ErrorTemporaryFailure, vacancyTestSubmitOperation, "HH vacancy test form does not contain a submission token", nil)
	}
	required := bool(test.Required) || state.VacancyResponsePopup.Vacancy.Test.Required
	fields := map[string]string{
		"_xsrf":        xsrf,
		"uidPk":        string(test.UIDPk),
		"guid":         test.GUID,
		"startTime":    jsonScalar(test.StartTime),
		"testRequired": strconv.FormatBool(required),
	}
	tasks := make([]vacancyTestFormTask, 0, len(test.Tasks))
	for _, task := range test.Tasks {
		id := strings.TrimSpace(string(task.ID))
		if id == "" {
			return vacancyTestCapture{}, vacancyTestError("HH vacancy test contains a task without an id")
		}
		formTask := vacancyTestFormTask{ID: id, Open: bool(task.Open), Multiple: bool(task.Multiple)}
		if len(task.CandidateSolutions) != 0 {
			formTask.OptionIDs = make(map[string]struct{}, len(task.CandidateSolutions))
			for _, solution := range task.CandidateSolutions {
				optionID := strings.TrimSpace(string(solution.ID))
				if optionID == "" {
					return vacancyTestCapture{}, vacancyTestError(fmt.Sprintf("HH vacancy task %q contains an option without an id", id))
				}
				formTask.OptionIDs[optionID] = struct{}{}
			}
		}
		tasks = append(tasks, formTask)
	}
	return vacancyTestCapture{
		Questionnaire: questionnaire, ActionURL: actionURL, Fields: fields, Tasks: tasks,
		ResumeVisibility: state.VacancyResponsePopup.Vacancy.ResumeVisibility,
	}, nil
}

// SubmitVacancyTest fills the vacancy response popup with open-text and choice
// answers and verifies through read-back that HH accepted the submission. Code
// tasks remain unsupported; every captured task requires exactly one answer.
func (client *BrowserApplicationClient) SubmitVacancyTest(ctx context.Context, profileID core.ProfileID, key core.VacancyKey, answers []core.ResolvedAnswer) error {
	if err := client.validateIdentity(profileID, key); err != nil {
		return err
	}
	if len(answers) == 0 {
		return vacancyTestError("HH vacancy test submission requires answers")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	actionURL := vacancyResponsePageURL(client.webBaseURL, key.ExternalID)
	document, finalURL, err := client.reader.getHTML(ctx, actionURL, vacancyTestSubmitOperation)
	if err != nil {
		return err
	}
	if isLoginURL(finalURL) {
		return operationError(core.ErrorUnauthorized, vacancyTestSubmitOperation, "HH browser session requires authentication", nil)
	}
	rendered, err := renderHTMLNode(document)
	if err != nil {
		return operationError(core.ErrorTemporaryFailure, vacancyTestSubmitOperation, "HH popup could not be rendered", err)
	}
	capture, err := parseVacancyTestCapture(rendered, actionURL)
	if err != nil {
		return err
	}
	if err := resumeVisibilityBlocker(capture.ResumeVisibility, client.options.ResumeID, vacancyTestSubmitOperation); err != nil {
		return err
	}
	form := url.Values{}
	for name, value := range capture.Fields {
		if value != "" {
			form.Set(name, value)
		}
	}
	tasksByID := make(map[string]vacancyTestFormTask, len(capture.Tasks))
	for _, task := range capture.Tasks {
		tasksByID[task.ID] = task
	}
	answered := make(map[string]bool, len(answers))
	for _, answer := range answers {
		taskID := strings.TrimSpace(answer.QuestionID)
		task, ok := tasksByID[taskID]
		if !ok {
			return vacancyTestError(fmt.Sprintf("HH vacancy task %q is not part of the questionnaire", answer.QuestionID))
		}
		if answered[taskID] {
			return vacancyTestError(fmt.Sprintf("HH vacancy task %q has more than one answer", answer.QuestionID))
		}
		answered[taskID] = true
		if err := fillVacancyTestTask(form, task, answer); err != nil {
			return err
		}
	}
	for _, task := range capture.Tasks {
		if !answered[task.ID] {
			return vacancyTestError(fmt.Sprintf("HH vacancy task %q has no answer", task.ID))
		}
	}
	httpClient, err := client.authenticatedClient(actionURL)
	if err != nil {
		return err
	}
	copy := *httpClient
	copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, actionURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create HH vacancy test request: %w", err)
	}
	client.setBrowserHeaders(request, "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Referer", strings.TrimRight(client.webBaseURL, "/")+"/vacancy/"+url.PathEscape(key.ExternalID))
	if xsrf := form.Get("_xsrf"); xsrf != "" {
		request.Header.Set("X-Xsrftoken", xsrf)
	}
	response, err := copy.Do(request)
	if err != nil {
		return operationError(core.ErrorAmbiguousResult, vacancyTestSubmitOperation, "HH vacancy test outcome is unknown after transport failure", err)
	}
	defer response.Body.Close()
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, maxBrowserApplicationResponse))
	if readErr != nil {
		return operationError(core.ErrorAmbiguousResult, vacancyTestSubmitOperation, "HH vacancy test outcome is unknown after reading the response", readErr)
	}
	if isLoginURL(response.Request.URL) {
		return operationError(core.ErrorUnauthorized, vacancyTestSubmitOperation, "HH browser session requires authentication", nil)
	}
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return operationError(core.ErrorUnauthorized, vacancyTestSubmitOperation, "HH browser session was rejected", nil)
	case response.StatusCode == http.StatusTooManyRequests:
		failure := operationError(core.ErrorRateLimited, vacancyTestSubmitOperation, "HH vacancy test submission was rate limited", nil)
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return failure
	case response.StatusCode >= 500:
		return operationError(core.ErrorTemporaryFailure, vacancyTestSubmitOperation, fmt.Sprintf("HH returned status %d", response.StatusCode), nil)
	default:
		return operationError(core.ErrorPermanentFailure, vacancyTestSubmitOperation, fmt.Sprintf("HH rejected the vacancy test with status %d", response.StatusCode), nil)
	}
	return client.confirmVacancyTestSubmission(ctx, actionURL)
}

// fillVacancyTestTask maps one resolved answer onto the live form fields. A
// choice task uses one value per selected option under "task_<id>"; an open
// task that also lists options is answered through the HH "open" branch.
func fillVacancyTestTask(form url.Values, task vacancyTestFormTask, answer core.ResolvedAnswer) error {
	text := strings.TrimSpace(answer.Text)
	options := answer.SelectedOptionIDs
	switch {
	case task.Open && len(task.OptionIDs) == 0:
		if text == "" || len(options) != 0 {
			return vacancyTestError(fmt.Sprintf("HH vacancy task %q requires an open-text answer", answer.QuestionID))
		}
		form.Set("task_"+task.ID+"_text", answer.Text)
	case task.Open:
		if text == "" || len(options) != 0 {
			return vacancyTestError(fmt.Sprintf("HH vacancy task %q requires a custom text answer", answer.QuestionID))
		}
		form.Set("task_"+task.ID, "open")
		form.Set("task_"+task.ID+"_text", answer.Text)
	case len(task.OptionIDs) != 0:
		if text != "" || len(options) == 0 {
			return vacancyTestError(fmt.Sprintf("HH vacancy task %q requires selected options", answer.QuestionID))
		}
		if !task.Multiple && len(options) != 1 {
			return vacancyTestError(fmt.Sprintf("HH vacancy task %q is single-choice and requires exactly one option", answer.QuestionID))
		}
		for _, optionID := range options {
			if _, ok := task.OptionIDs[optionID]; !ok {
				return vacancyTestError(fmt.Sprintf("HH vacancy task %q has no option %q", answer.QuestionID, optionID))
			}
			form.Add("task_"+task.ID, optionID)
		}
	default:
		return operationError(core.ErrorUnsupported, vacancyTestSubmitOperation, fmt.Sprintf("HH vacancy task %q is not supported by the browser submitter", answer.QuestionID), nil)
	}
	return nil
}

func (client *BrowserApplicationClient) confirmVacancyTestSubmission(ctx context.Context, actionURL string) error {
	document, finalURL, err := client.reader.getHTML(ctx, actionURL, vacancyTestSubmitOperation)
	if err != nil {
		return operationError(core.ErrorAmbiguousResult, vacancyTestSubmitOperation, "HH did not confirm the vacancy test submission", err)
	}
	if isLoginURL(finalURL) {
		return operationError(core.ErrorUnauthorized, vacancyTestSubmitOperation, "HH browser session requires authentication", nil)
	}
	rendered, err := renderHTMLNode(document)
	if err != nil {
		return operationError(core.ErrorAmbiguousResult, vacancyTestSubmitOperation, "HH did not confirm the vacancy test submission", err)
	}
	if _, _, err := parseVacancyTestState(rendered); err == nil {
		return vacancyTestError("HH did not confirm the vacancy test submission")
	}
	return nil
}

func vacancyTestError(message string) error {
	return &core.OperationError{Category: core.ErrorValidationRequired, Operation: vacancyTestSubmitOperation, Platform: Name, Message: message}
}

// jsonScalar renders a JSON string/number/bool as a plain form value.
func jsonScalar(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return value
}

func htmlInputValue(document []byte, name string) string {
	root, err := html.Parse(bytes.NewReader(document))
	if err != nil {
		return ""
	}
	node := findHTMLNode(root, func(node *html.Node) bool {
		return node.Type == html.ElementNode && node.Data == "input" && htmlAttribute(node, "name") == name
	})
	if node == nil {
		return ""
	}
	return htmlAttribute(node, "value")
}
