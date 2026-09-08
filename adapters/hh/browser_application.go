package hh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const maxBrowserApplicationResponse = 16 << 20

// BrowserApplicationClient is an explicitly bound write transport. Merely
// binding BrowserReadClient never grants permission to submit an application.
// Every POST is preceded by a fresh, idempotent popup preflight.
type BrowserApplicationClient struct {
	reader     *BrowserReadClient
	options    adapter.BrowserApplicationOptions
	webBaseURL string
	mu         sync.Mutex
}

var _ adapter.ApplicationTransport = (*BrowserApplicationClient)(nil)
var _ adapter.ApplicationReconciler = (*BrowserApplicationClient)(nil)
var _ adapter.SuitableResumeReader = (*BrowserApplicationClient)(nil)
var _ adapter.VacancyReader = (*BrowserApplicationClient)(nil)

type browserApplicationPreflight struct {
	Type                                string                           `json:"type"`
	RedirectURI                         string                           `json:"redirectUri"`
	RedirectURISnake                    string                           `json:"redirect_uri"`
	ResponseStatus                      browserApplicationResponseStatus `json:"responseStatus"`
	Body                                *browserApplicationPreflightBody `json:"body"`
	CountriesProfileVisibilityAgreement browserVisibilityAgreement       `json:"countriesProfileVisibilityAgreement"`
}

type browserApplicationPreflightBody struct {
	ResponseStatus browserApplicationResponseStatus `json:"responseStatus"`
}

type browserApplicationResponseStatus struct {
	Test                browserApplicationTest              `json:"test"`
	LetterMaxLength     int                                 `json:"letterMaxLength"`
	UsedResumeIDs       flexibleIDs                         `json:"usedResumeIds"`
	UnusedResumeIDs     flexibleIDs                         `json:"unusedResumeIds"`
	UnfinishedResumeIDs flexibleIDs                         `json:"unfinishedResumeIds"`
	HiddenResumeIDs     flexibleIDs                         `json:"hiddenResumeIds"`
	Resumes             map[string]browserApplicationResume `json:"resumes"`
	ResponseImpossible  bool                                `json:"responseImpossible"`
	AlreadyApplied      bool                                `json:"alreadyApplied"`
	HasQuickResponse    bool                                `json:"hasQuickResponse"`
	Negotiations        json.RawMessage                     `json:"negotiations"`
	ResumeVisibility    json.RawMessage                     `json:"resumeVisibility"`
}

type browserApplicationTest struct {
	HasTests bool `json:"hasTests"`
	Required bool `json:"required"`
}

type browserApplicationResume struct {
	ID           flexibleID      `json:"id"`
	Hash         string          `json:"hash"`
	Title        json.RawMessage `json:"title"`
	IsIncomplete bool            `json:"isIncomplete"`
	Forbidden    json.RawMessage `json:"forbidden"`
}

type browserVisibilityAgreement struct {
	Show                        bool        `json:"show"`
	Confirmed                   bool        `json:"confirmed"`
	ConfirmedCountryIDs         flexibleIDs `json:"confirmedCountryIds"`
	RequiredCountryVisibilityID flexibleID  `json:"requiredCountryVisibilityId"`
}

type flexibleID string

func (id *flexibleID) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*id = ""
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*id = flexibleID(text)
		return nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return errors.New("identifier must be a string or number")
	}
	*id = flexibleID(number.String())
	return nil
}

type flexibleIDs []string

func (ids *flexibleIDs) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*ids = nil
		return nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		var id flexibleID
		if err := id.UnmarshalJSON(value); err != nil {
			return err
		}
		if strings.TrimSpace(string(id)) != "" {
			result = append(result, string(id))
		}
	}
	*ids = result
	return nil
}

func NewBrowserApplicationClient(profileID core.ProfileID, stateFile, userAgent string, httpClient *http.Client, options adapter.BrowserApplicationOptions) (*BrowserApplicationClient, error) {
	reader, err := NewBrowserReadClient(profileID, stateFile, userAgent, httpClient)
	if err != nil {
		return nil, err
	}
	return newBrowserApplicationClient(reader, options), nil
}

func newBrowserApplicationClient(reader *BrowserReadClient, options adapter.BrowserApplicationOptions) *BrowserApplicationClient {
	return &BrowserApplicationClient{reader: reader, options: options, webBaseURL: reader.webBaseURL}
}

func (client *BrowserApplicationClient) ReadVacancy(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) (core.Vacancy, error) {
	return client.reader.ReadVacancy(ctx, profileID, key)
}

func (client *BrowserApplicationClient) ListSuitableResumes(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) ([]adapter.SuitableResume, error) {
	if err := client.validateIdentity(profileID, key); err != nil {
		return nil, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	preflight, err := client.preflight(ctx, key)
	if err != nil {
		return nil, err
	}
	if preflightAlreadyApplied(preflight) {
		ids := append([]string(nil), preflight.ResponseStatus.UsedResumeIDs...)
		if configured := strings.TrimSpace(client.options.ResumeID); configured != "" && !contains(ids, configured) {
			ids = append(ids, configured)
		}
		result := make([]adapter.SuitableResume, 0, len(ids))
		for _, id := range ids {
			result = append(result, adapter.SuitableResume{ID: id})
		}
		return result, nil
	}
	if err := client.preflightBlocker(preflight, "vacancies.suitable_resumes.browser"); err != nil {
		return nil, err
	}

	hidden := stringSet(preflight.ResponseStatus.HiddenResumeIDs)
	unfinished := stringSet(preflight.ResponseStatus.UnfinishedResumeIDs)
	seen := make(map[string]struct{})
	result := make([]adapter.SuitableResume, 0, len(preflight.ResponseStatus.Resumes))
	for key, resume := range preflight.ResponseStatus.Resumes {
		aliases := resumeAliases(key, resume)
		if resume.IsIncomplete || rawTruthy(resume.Forbidden) || intersects(aliases, hidden) || intersects(aliases, unfinished) {
			continue
		}
		for _, id := range aliases {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			result = append(result, adapter.SuitableResume{ID: id, Title: browserResumeTitle(resume.Title)})
		}
	}
	return result, nil
}

func (client *BrowserApplicationClient) SubmitApplication(ctx context.Context, command adapter.ApplicationSubmitCommand) (adapter.ApplicationSubmitResult, error) {
	if err := validateApplicationCommand(client.reader.profileID, command); err != nil {
		return adapter.ApplicationSubmitResult{}, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()

	preflight, err := client.preflight(ctx, command.Vacancy)
	if err != nil {
		return adapter.ApplicationSubmitResult{}, err
	}
	if preflightAlreadyApplied(preflight) || contains(preflight.ResponseStatus.UsedResumeIDs, command.ResumeID) {
		return adapter.ApplicationSubmitResult{
			ExternalNegotiationID: negotiationID(preflight.ResponseStatus.Negotiations), Applied: true, AlreadyApplied: true,
		}, nil
	}
	if err := client.preflightBlocker(preflight, "applications.submit.browser"); err != nil {
		return adapter.ApplicationSubmitResult{}, err
	}
	resumeKey, resume, ok := findBrowserResume(preflight.ResponseStatus.Resumes, command.ResumeID)
	if !ok {
		return adapter.ApplicationSubmitResult{}, browserReviewError(core.ErrorValidationRequired, "resume_not_suitable", "configured resume is not available for this vacancy")
	}
	aliases := resumeAliases(resumeKey, resume)
	if resume.IsIncomplete || rawTruthy(resume.Forbidden) || intersects(aliases, stringSet(preflight.ResponseStatus.UnfinishedResumeIDs)) || intersects(aliases, stringSet(preflight.ResponseStatus.HiddenResumeIDs)) {
		return adapter.ApplicationSubmitResult{}, browserReviewError(core.ErrorValidationRequired, "resume_not_suitable", "configured resume cannot be used for this vacancy")
	}
	if containsAny(preflight.ResponseStatus.UsedResumeIDs, aliases) {
		return adapter.ApplicationSubmitResult{ExternalNegotiationID: negotiationID(preflight.ResponseStatus.Negotiations), Applied: true, AlreadyApplied: true}, nil
	}
	if preflight.ResponseStatus.LetterMaxLength > 0 && len([]rune(command.Message)) > preflight.ResponseStatus.LetterMaxLength {
		return adapter.ApplicationSubmitResult{}, browserReviewError(core.ErrorValidationRequired, "cover_letter_too_long", "cover letter exceeds the current HH form limit")
	}

	resumeHash := strings.TrimSpace(resume.Hash)
	if resumeHash == "" {
		return adapter.ApplicationSubmitResult{}, browserReviewError(core.ErrorValidationRequired, "resume_hash_missing", "HH popup did not return a submit hash for the configured resume")
	}
	countryIDs := make([]string, len(preflight.CountriesProfileVisibilityAgreement.ConfirmedCountryIDs))
	copy(countryIDs, preflight.CountriesProfileVisibilityAgreement.ConfirmedCountryIDs)
	if required := strings.TrimSpace(string(preflight.CountriesProfileVisibilityAgreement.RequiredCountryVisibilityID)); required != "" && !contains(countryIDs, required) {
		countryIDs = append(countryIDs, required)
	}
	encodedCountryIDs, err := json.Marshal(countryIDs)
	if err != nil {
		return adapter.ApplicationSubmitResult{}, fmt.Errorf("encode HH visibility countries: %w", err)
	}

	fields := map[string]string{
		"vacancy_id":       command.Vacancy.ExternalID,
		"resume_hash":      resumeHash,
		"ignore_postponed": "true",
		"incomplete":       strconv.FormatBool(resume.IsIncomplete),
		"mark_applicant_visible_in_vacancy_country": strconv.FormatBool(preflight.CountriesProfileVisibilityAgreement.Show && client.options.AllowVisibilityChange),
		"country_ids": string(encodedCountryIDs),
		"letter":      command.Message,
		"lux":         "true",
		"withoutTest": "no",
	}
	return client.postApplication(ctx, command, fields)
}

func (client *BrowserApplicationClient) ReconcileApplication(ctx context.Context, command adapter.ApplicationReconcileCommand) (adapter.ApplicationReconcileResult, error) {
	if err := client.validateIdentity(command.ProfileID, command.Vacancy); err != nil || strings.TrimSpace(command.ResumeID) == "" {
		if err != nil {
			return adapter.ApplicationReconcileResult{}, err
		}
		return adapter.ApplicationReconcileResult{}, errors.New("HH browser application reconciliation requires resume")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	preflight, err := client.preflight(ctx, command.Vacancy)
	if err != nil {
		return adapter.ApplicationReconcileResult{}, err
	}
	resumeKey, resume, found := findBrowserResume(preflight.ResponseStatus.Resumes, command.ResumeID)
	applied := preflightAlreadyApplied(preflight) || contains(preflight.ResponseStatus.UsedResumeIDs, command.ResumeID)
	if found {
		applied = applied || containsAny(preflight.ResponseStatus.UsedResumeIDs, resumeAliases(resumeKey, resume))
	}
	if id := negotiationID(preflight.ResponseStatus.Negotiations); id != "" {
		applied = true
		return adapter.ApplicationReconcileResult{Applied: applied, ExternalNegotiationID: id}, nil
	}
	return adapter.ApplicationReconcileResult{Applied: applied}, nil
}

func (client *BrowserApplicationClient) validateIdentity(profileID core.ProfileID, key core.VacancyKey) error {
	if profileID == "" || profileID != client.reader.profileID {
		return errors.New("HH browser application profile does not match")
	}
	if err := key.Validate(); err != nil {
		return err
	}
	if key.Platform != Name {
		return errors.New("HH browser application requires an HH vacancy")
	}
	return nil
}

func (client *BrowserApplicationClient) preflight(ctx context.Context, key core.VacancyKey) (browserApplicationPreflight, error) {
	endpoint, _ := url.Parse(strings.TrimRight(client.webBaseURL, "/") + "/applicant/vacancy_response/popup")
	query := endpoint.Query()
	query.Set("vacancyId", key.ExternalID)
	query.Set("isTest", "no")
	query.Set("withoutTest", "no")
	query.Set("lux", "true")
	query.Set("alreadyApplied", "false")
	endpoint.RawQuery = query.Encode()

	httpClient, err := client.authenticatedClient(endpoint.String())
	if err != nil {
		return browserApplicationPreflight{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return browserApplicationPreflight{}, fmt.Errorf("create HH application preflight request: %w", err)
	}
	client.setBrowserHeaders(request, "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return browserApplicationPreflight{}, ctxErr
		}
		return browserApplicationPreflight{}, operationError(core.ErrorTemporaryFailure, "applications.preflight.browser", "HH application preflight failed", err)
	}
	defer response.Body.Close()
	if isLoginURL(response.Request.URL) {
		drain(response.Body)
		return browserApplicationPreflight{}, operationError(core.ErrorUnauthorized, "applications.preflight.browser", "HH browser session requires authentication", nil)
	}
	if err := classifyBrowserApplicationGET(response); err != nil {
		return browserApplicationPreflight{}, err
	}
	var payload browserApplicationPreflight
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBrowserApplicationResponse))
	if err := decoder.Decode(&payload); err != nil {
		return browserApplicationPreflight{}, operationError(core.ErrorTemporaryFailure, "applications.preflight.browser", "HH returned an invalid application preflight", err)
	}
	// HH currently wraps the vacancy response state in body.responseStatus,
	// while older responses and fixtures expose responseStatus at the top
	// level. Normalize both shapes at the adapter boundary so the application
	// workflow never interprets a successfully decoded wrapper as an empty
	// suitable-resume list.
	if payload.Body != nil {
		payload.ResponseStatus = payload.Body.ResponseStatus
	}
	return payload, nil
}

func (client *BrowserApplicationClient) preflightBlocker(preflight browserApplicationPreflight, operation string) error {
	kind := browserFlowKind(preflight.Type)
	status := preflight.ResponseStatus
	if kind == "testrequired" || status.Test.HasTests || status.Test.Required {
		return browserReviewErrorAt(operation, core.ErrorValidationRequired, "questionnaire_required", "HH vacancy requires a test or questionnaire")
	}
	if status.ResponseImpossible {
		return browserReviewErrorAt(operation, core.ErrorValidationRequired, "response_impossible", "HH currently does not allow an application to this vacancy")
	}
	if preflight.CountriesProfileVisibilityAgreement.Show && !client.options.AllowVisibilityChange {
		return browserReviewErrorAt(operation, core.ErrorConfirmationRequired, "resume_visibility_change_required", "HH requires an explicit resume visibility change")
	}
	switch kind {
	case "", "modal", "quickresponse", "alreadyapplied":
		return nil
	case "needlogin", "login":
		return operationError(core.ErrorUnauthorized, operation, "HH browser session requires authentication", nil)
	case "noresumes":
		return browserReviewErrorAt(operation, core.ErrorValidationRequired, "resume_not_suitable", "HH has no resume available for this vacancy")
	default:
		return browserReviewErrorAt(operation, core.ErrorValidationRequired, "unsupported_response_flow", "HH requires an unsupported application flow: "+kind)
	}
}

func browserFlowKind(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer("-", "", "_", "", " ", "")
	return replacer.Replace(value)
}

func preflightAlreadyApplied(preflight browserApplicationPreflight) bool {
	return preflight.ResponseStatus.AlreadyApplied || browserFlowKind(preflight.Type) == "alreadyapplied"
}

func (client *BrowserApplicationClient) postApplication(ctx context.Context, command adapter.ApplicationSubmitCommand, fields map[string]string) (adapter.ApplicationSubmitResult, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return adapter.ApplicationSubmitResult{}, fmt.Errorf("encode HH browser application field %s: %w", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		return adapter.ApplicationSubmitResult{}, fmt.Errorf("finish HH browser application body: %w", err)
	}
	endpoint := strings.TrimRight(client.webBaseURL, "/") + "/applicant/vacancy_response/popup"
	httpClient, err := client.authenticatedClient(endpoint)
	if err != nil {
		return adapter.ApplicationSubmitResult{}, err
	}
	copy := *httpClient
	copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return adapter.ApplicationSubmitResult{}, fmt.Errorf("create HH browser application request: %w", err)
	}
	client.setBrowserHeaders(request, "application/json")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Referer", strings.TrimRight(client.webBaseURL, "/")+"/vacancy/"+url.PathEscape(command.Vacancy.ExternalID))
	if xsrf := cookieValue(httpClient, request.URL, "_xsrf"); xsrf != "" {
		request.Header.Set("X-Xsrftoken", xsrf)
	}
	response, err := copy.Do(request)
	if err != nil {
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorAmbiguousResult, "applications.submit.browser", "HH browser application outcome is unknown after transport failure", err)
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxBrowserApplicationResponse))
	if readErr != nil {
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorAmbiguousResult, "applications.submit.browser", "HH browser application outcome is unknown after reading the response", readErr)
	}
	return classifyBrowserApplicationPOST(response, data)
}

func (client *BrowserApplicationClient) authenticatedClient(endpoint string) (*http.Client, error) {
	transport, err := NewResumeTouchTransport(client.reader.stateFile, client.reader.httpClient)
	if err != nil {
		return nil, err
	}
	transport.profileURL = endpoint
	transport.touchURL = endpoint
	return transport.authenticatedClient()
}

func (client *BrowserApplicationClient) setBrowserHeaders(request *http.Request, accept string) {
	request.Header.Set("Accept", accept)
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	request.Header.Set("User-Agent", client.reader.userAgent)
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
}

func classifyBrowserApplicationGET(response *http.Response) error {
	operation := "applications.preflight.browser"
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		drain(response.Body)
		return operationError(core.ErrorUnauthorized, operation, "HH browser session was rejected", nil)
	case response.StatusCode == http.StatusTooManyRequests:
		drain(response.Body)
		failure := operationError(core.ErrorRateLimited, operation, "HH application preflight was rate limited", nil)
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return failure
	case response.StatusCode >= 500:
		drain(response.Body)
		return operationError(core.ErrorTemporaryFailure, operation, fmt.Sprintf("HH returned status %d", response.StatusCode), nil)
	default:
		drain(response.Body)
		return operationError(core.ErrorPermanentFailure, operation, fmt.Sprintf("HH rejected application preflight with status %d", response.StatusCode), nil)
	}
}

func classifyBrowserApplicationPOST(response *http.Response, data []byte) (adapter.ApplicationSubmitResult, error) {
	operation := "applications.submit.browser"
	var payload map[string]any
	if len(bytes.TrimSpace(data)) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		_ = decoder.Decode(&payload)
	}
	alreadyApplied := nestedBool(payload, "alreadyApplied")
	externalID := firstNestedID(payload, "topic_id", "topicId", "chat_id", "chatId", "negotiation_id", "negotiationId")
	if alreadyApplied {
		return adapter.ApplicationSubmitResult{ExternalNegotiationID: externalID, Applied: true, AlreadyApplied: true}, nil
	}
	if failure := browserApplicationPayloadError(payload); failure != "" {
		return classifyBrowserApplicationPayloadFailure(operation, failure)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if nestedBool(payload, "success") || externalID != "" {
			return adapter.ApplicationSubmitResult{ExternalNegotiationID: externalID, Applied: true}, nil
		}
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorAmbiguousResult, operation, "HH returned no definitive browser application result", nil)
	}
	if response.StatusCode == http.StatusTooManyRequests {
		failure := operationError(core.ErrorRateLimited, operation, "HH browser application was rate limited", nil)
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return adapter.ApplicationSubmitResult{}, failure
	}
	if response.StatusCode == http.StatusUnauthorized {
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorUnauthorized, operation, "HH rejected browser application authorization", nil)
	}
	if response.StatusCode >= 500 || response.StatusCode >= 300 && response.StatusCode < 400 {
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorAmbiguousResult, operation, fmt.Sprintf("HH browser application outcome is unknown after status %d", response.StatusCode), nil)
	}
	if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnprocessableEntity {
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorValidationRequired, operation, "HH rejected browser application input", nil)
	}
	return adapter.ApplicationSubmitResult{}, operationError(core.ErrorPermanentFailure, operation, fmt.Sprintf("HH rejected browser application with status %d", response.StatusCode), nil)
}

func classifyBrowserApplicationPayloadFailure(operation, value string) (adapter.ApplicationSubmitResult, error) {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "-", "_"), " ", "_"))
	switch {
	case strings.Contains(normalized, "already") && strings.Contains(normalized, "appl"):
		return adapter.ApplicationSubmitResult{Applied: true, AlreadyApplied: true}, nil
	case strings.Contains(normalized, "captcha"):
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorConfirmationRequired, operation, "HH requires captcha confirmation", nil)
	case strings.Contains(normalized, "test_required") || strings.Contains(normalized, "questionnaire"):
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorValidationRequired, operation, "HH vacancy requires a test or questionnaire", nil)
	case strings.Contains(normalized, "limit"):
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorQuotaExceeded, operation, "HH application quota is exhausted", nil)
	case strings.Contains(normalized, "letter"), strings.Contains(normalized, "resume"), strings.Contains(normalized, "visibility"),
		strings.Contains(normalized, "spam"), strings.Contains(normalized, "resource_policy"), strings.Contains(normalized, "inappropriate"):
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorValidationRequired, operation, "HH rejected browser application input: "+normalized, nil)
	default:
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorPermanentFailure, operation, "HH rejected browser application: "+normalized, nil)
	}
}

func browserApplicationPayloadError(payload map[string]any) string {
	for _, key := range []string{"error", "errorCode", "error_code"} {
		if value, ok := payload[key].(string); ok && value != "" {
			return value
		}
	}
	if errorsValue, ok := payload["errors"].([]any); ok {
		for _, item := range errorsValue {
			if values, ok := item.(map[string]any); ok {
				for _, key := range []string{"value", "type", "message"} {
					if value, ok := values[key].(string); ok && value != "" {
						return value
					}
				}
			}
		}
	}
	return ""
}

func findBrowserResume(resumes map[string]browserApplicationResume, expected string) (string, browserApplicationResume, bool) {
	for key, resume := range resumes {
		for _, alias := range resumeAliases(key, resume) {
			if alias == expected {
				return key, resume, true
			}
		}
	}
	return "", browserApplicationResume{}, false
}

func resumeAliases(key string, resume browserApplicationResume) []string {
	values := []string{strings.TrimSpace(key), strings.TrimSpace(string(resume.ID)), strings.TrimSpace(resume.Hash)}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func rawTruthy(value json.RawMessage) bool {
	if len(value) == 0 {
		return false
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return true
	}
	switch item := decoded.(type) {
	case nil:
		return false
	case bool:
		return item
	case string:
		return strings.TrimSpace(item) != ""
	case json.Number:
		return item.String() != "0"
	case []any:
		return len(item) != 0
	case map[string]any:
		return len(item) != 0
	default:
		return true
	}
}

func browserResumeTitle(value json.RawMessage) string {
	if len(value) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var parts []any
	if err := json.Unmarshal(value, &parts); err != nil {
		return ""
	}
	for _, part := range parts {
		switch item := part.(type) {
		case string:
			if item = strings.TrimSpace(item); item != "" {
				return item
			}
		case map[string]any:
			for _, key := range []string{"string", "text", "value"} {
				if itemText, ok := item[key].(string); ok && strings.TrimSpace(itemText) != "" {
					return strings.TrimSpace(itemText)
				}
			}
		}
	}
	return ""
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func intersects(values []string, set map[string]struct{}) bool {
	for _, value := range values {
		if _, exists := set[value]; exists {
			return true
		}
	}
	return false
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsAny(values []string, expected []string) bool {
	set := stringSet(values)
	return intersects(expected, set)
}

func browserReviewError(category core.ErrorCategory, code, message string) error {
	return browserReviewErrorAt("applications.submit.browser", category, code, message)
}

func browserReviewErrorAt(operation string, category core.ErrorCategory, code, message string) error {
	return &core.OperationError{Category: category, Operation: operation, Platform: Name, Message: message, Metadata: map[string]string{"code": code}}
}

func negotiationID(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return ""
	}
	return firstNestedID(value, "topic_id", "topicId", "chat_id", "chatId", "id")
}

func firstNestedID(value any, keys ...string) string {
	keySet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		keySet[key] = struct{}{}
	}
	var walk func(any) string
	walk = func(current any) string {
		switch item := current.(type) {
		case map[string]any:
			for _, key := range keys {
				if child, exists := item[key]; exists {
					if result := scalarID(child); result != "" {
						return result
					}
				}
			}
			for key, child := range item {
				if _, direct := keySet[key]; direct {
					continue
				}
				if result := walk(child); result != "" {
					return result
				}
			}
		case []any:
			for _, child := range item {
				if result := walk(child); result != "" {
					return result
				}
			}
		}
		return ""
	}
	return walk(value)
}

func scalarID(value any) string {
	switch item := value.(type) {
	case string:
		return strings.TrimSpace(item)
	case json.Number:
		return item.String()
	case float64:
		return strconv.FormatFloat(item, 'f', -1, 64)
	default:
		return ""
	}
}

func nestedBool(value any, key string) bool {
	switch item := value.(type) {
	case map[string]any:
		if result, ok := item[key].(bool); ok && result {
			return true
		}
		for _, child := range item {
			if nestedBool(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if nestedBool(child, key) {
				return true
			}
		}
	}
	return false
}
