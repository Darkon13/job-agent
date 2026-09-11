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
	Questionnaire core.Questionnaire
	ActionURL     string
	Fields        map[string]string
	TextFields    map[string]string
}

func vacancyResponsePageURL(baseURL, externalID string) string {
	return strings.TrimRight(baseURL, "/") + "/applicant/vacancy_response?" + url.Values{
		"vacancyId":           []string{externalID},
		"startedWithQuestion": []string{"false"},
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
	required := test.Required || state.VacancyResponsePopup.Vacancy.Test.Required
	fields := map[string]string{
		"_xsrf":        xsrf,
		"uidPk":        string(test.UIDPk),
		"guid":         test.GUID,
		"startTime":    jsonScalar(test.StartTime),
		"testRequired": strconv.FormatBool(required),
	}
	textFields := make(map[string]string, len(test.Tasks))
	for _, task := range test.Tasks {
		if !task.Open {
			continue
		}
		id := strings.TrimSpace(string(task.ID))
		if id != "" {
			textFields[id] = "task_" + id + "_text"
		}
	}
	if len(textFields) == 0 {
		return vacancyTestCapture{}, operationError(core.ErrorUnsupported, vacancyTestSubmitOperation, "HH vacancy test has no open-text tasks that the adapter can submit", nil)
	}
	return vacancyTestCapture{Questionnaire: questionnaire, ActionURL: actionURL, Fields: fields, TextFields: textFields}, nil
}

// SubmitVacancyTest fills the vacancy response popup with open-text answers and
// verifies through read-back that HH accepted the submission. Choice and code
// tasks remain unsupported until their form fields are verified live.
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
	form := url.Values{}
	for name, value := range capture.Fields {
		if value != "" {
			form.Set(name, value)
		}
	}
	answered := make(map[string]bool, len(answers))
	for _, answer := range answers {
		taskID := strings.TrimSpace(answer.QuestionID)
		field, ok := capture.TextFields[taskID]
		if !ok {
			return vacancyTestError(fmt.Sprintf("HH vacancy task %q is not an open-text task", answer.QuestionID))
		}
		if strings.TrimSpace(answer.Text) == "" {
			return vacancyTestError(fmt.Sprintf("HH vacancy task %q requires an open-text answer", answer.QuestionID))
		}
		if len(answer.SelectedOptionIDs) != 0 {
			return vacancyTestError(fmt.Sprintf("HH vacancy task %q must not select options", answer.QuestionID))
		}
		form.Set(field, answer.Text)
		answered[taskID] = true
	}
	for taskID := range capture.TextFields {
		if !answered[taskID] {
			return vacancyTestError(fmt.Sprintf("HH vacancy task %q has no answer", taskID))
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
