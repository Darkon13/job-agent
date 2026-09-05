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
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const maximumApplicationMessageRunes = 10000

type hhErrorResponse struct {
	Errors []hhErrorItem `json:"errors"`
}

type hhErrorItem struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func (client *ReadClient) SubmitApplication(ctx context.Context, command adapter.ApplicationSubmitCommand) (adapter.ApplicationSubmitResult, error) {
	if err := validateApplicationCommand(client.profileID, command); err != nil {
		return adapter.ApplicationSubmitResult{}, err
	}
	select {
	case <-ctx.Done():
		return adapter.ApplicationSubmitResult{}, ctx.Err()
	case <-client.profileLock:
	}
	defer func() { client.profileLock <- struct{}{} }()

	credentials, err := loadOAuthCredentials(client.credentialsRef)
	if err != nil {
		return adapter.ApplicationSubmitResult{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"resume_id": command.ResumeID, "vacancy_id": command.Vacancy.ExternalID, "message": command.Message,
	} {
		if value == "" && key == "message" {
			continue
		}
		if err := writer.WriteField(key, value); err != nil {
			return adapter.ApplicationSubmitResult{}, fmt.Errorf("encode HH application field %s: %w", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		return adapter.ApplicationSubmitResult{}, fmt.Errorf("finish HH application body: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(client.apiBaseURL, "/")+"/negotiations", &body)
	if err != nil {
		return adapter.ApplicationSubmitResult{}, fmt.Errorf("create HH application request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	request.Header.Set("HH-User-Agent", client.userAgent)
	request.Header.Set("Content-Type", writer.FormDataContentType())

	httpClient := *client.httpClient
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return adapter.ApplicationSubmitResult{}, operationError(core.ErrorAmbiguousResult, "applications.submit", "HH application outcome is unknown after request interruption", err)
		}
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorAmbiguousResult, "applications.submit", "HH application outcome is unknown after transport failure", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusCreated {
		drain(response.Body)
		negotiationID, err := negotiationIDFromLocation(response.Header.Get("Location"))
		if err != nil {
			return adapter.ApplicationSubmitResult{}, operationError(core.ErrorAmbiguousResult, "applications.submit", "HH accepted application without a usable negotiation location", err)
		}
		return adapter.ApplicationSubmitResult{ExternalNegotiationID: negotiationID}, nil
	}
	if response.StatusCode == http.StatusSeeOther {
		drain(response.Body)
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorUnsupported, "applications.submit", "HH requires direct application outside the public API", nil)
	}
	failure := readHHError(response.Body)
	return classifyApplicationFailure(response, failure)
}

func (client *ReadClient) ReconcileApplication(ctx context.Context, command adapter.ApplicationReconcileCommand) (adapter.ApplicationReconcileResult, error) {
	if command.ProfileID == "" || command.ProfileID != client.profileID || command.Vacancy.Platform != Name ||
		strings.TrimSpace(command.Vacancy.ExternalID) == "" || strings.TrimSpace(command.ResumeID) == "" {
		return adapter.ApplicationReconcileResult{}, errors.New("HH application reconciliation requires profile, vacancy and resume")
	}
	select {
	case <-ctx.Done():
		return adapter.ApplicationReconcileResult{}, ctx.Err()
	case <-client.profileLock:
	}
	defer func() { client.profileLock <- struct{}{} }()
	credentials, err := loadOAuthCredentials(client.credentialsRef)
	if err != nil {
		return adapter.ApplicationReconcileResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(client.apiBaseURL, "/")+"/vacancies/"+url.PathEscape(command.Vacancy.ExternalID), nil)
	if err != nil {
		return adapter.ApplicationReconcileResult{}, fmt.Errorf("create HH reconciliation request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	request.Header.Set("HH-User-Agent", client.userAgent)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return adapter.ApplicationReconcileResult{}, operationError(core.ErrorTemporaryFailure, "applications.reconcile", "read HH vacancy relations", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		var payload struct {
			Relations []string `json:"relations"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, maxAPIResponse)).Decode(&payload); err != nil {
			return adapter.ApplicationReconcileResult{}, operationError(core.ErrorTemporaryFailure, "applications.reconcile", "decode HH vacancy relations", err)
		}
		for _, relation := range payload.Relations {
			if relation == "got_response" {
				return adapter.ApplicationReconcileResult{Applied: true}, nil
			}
		}
		return adapter.ApplicationReconcileResult{Applied: false}, nil
	}
	failure := readHHError(response.Body)
	return adapter.ApplicationReconcileResult{}, classifyReconciliationFailure(response, failure)
}

func validateApplicationCommand(profileID core.ProfileID, command adapter.ApplicationSubmitCommand) error {
	if command.ProfileID == "" || command.ProfileID != profileID {
		return errors.New("HH application profile does not match")
	}
	if err := command.Vacancy.Validate(); err != nil {
		return err
	}
	if command.Vacancy.Platform != Name {
		return errors.New("HH application requires an HH vacancy")
	}
	if strings.TrimSpace(command.ResumeID) == "" || strings.TrimSpace(command.IdempotencyKey) == "" {
		return errors.New("HH application requires resume and idempotency key")
	}
	if !utf8.ValidString(command.Message) || utf8.RuneCountInString(command.Message) > maximumApplicationMessageRunes {
		return errors.New("HH application message must be valid UTF-8 and contain at most 10000 characters")
	}
	return nil
}

func negotiationIDFromLocation(value string) (string, error) {
	location, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	cleaned := path.Clean(location.Path)
	if !strings.Contains(cleaned, "/negotiations/") {
		return "", errors.New("location is not a negotiation resource")
	}
	id := path.Base(cleaned)
	if id == "." || id == "/" || id == "negotiations" || id == "" {
		return "", errors.New("location has no negotiation id")
	}
	return id, nil
}

func readHHError(reader io.Reader) hhErrorResponse {
	var response hhErrorResponse
	_ = json.NewDecoder(io.LimitReader(reader, maxAPIResponse)).Decode(&response)
	return response
}

func classifyApplicationFailure(response *http.Response, failure hhErrorResponse) (adapter.ApplicationSubmitResult, error) {
	operation := "applications.submit"
	if response.StatusCode == http.StatusTooManyRequests {
		err := operationError(core.ErrorRateLimited, operation, "HH application was rate limited", nil)
		err.RetryAfter = retryAfter(response.Header.Get("Retry-After"), timeNow())
		return adapter.ApplicationSubmitResult{}, err
	}
	for _, item := range failure.Errors {
		if item.Type == "oauth" {
			return adapter.ApplicationSubmitResult{}, operationError(core.ErrorUnauthorized, operation, "HH credentials are expired, revoked or invalid", nil)
		}
		if item.Type == "captcha_required" || item.Value == "captcha_required" {
			return adapter.ApplicationSubmitResult{}, operationError(core.ErrorConfirmationRequired, operation, "HH requires captcha confirmation", nil)
		}
		if item.Type != "negotiations" {
			continue
		}
		switch item.Value {
		case "already_applied":
			return adapter.ApplicationSubmitResult{AlreadyApplied: true}, nil
		case "test_required":
			return adapter.ApplicationSubmitResult{}, operationError(core.ErrorValidationRequired, operation, "HH vacancy requires a test or questionnaire", nil)
		case "limit_exceeded":
			return adapter.ApplicationSubmitResult{}, operationError(core.ErrorQuotaExceeded, operation, "HH application quota is exhausted", nil)
		case "resume_not_found", "empty_message", "too_long_message", "resume_visibility_conflict":
			return adapter.ApplicationSubmitResult{}, operationError(core.ErrorValidationRequired, operation, "HH rejected application input: "+item.Value, nil)
		case "vacancy_not_found", "invalid_vacancy", "application_denied":
			return adapter.ApplicationSubmitResult{}, operationError(core.ErrorPermanentFailure, operation, "HH rejected application: "+item.Value, nil)
		}
	}
	if response.StatusCode == http.StatusUnauthorized {
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorUnauthorized, operation, "HH rejected application authorization", nil)
	}
	if response.StatusCode >= 500 {
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorAmbiguousResult, operation, fmt.Sprintf("HH application outcome is unknown after status %d", response.StatusCode), nil)
	}
	if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnprocessableEntity {
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorValidationRequired, operation, "HH rejected application input", nil)
	}
	return adapter.ApplicationSubmitResult{}, operationError(core.ErrorPermanentFailure, operation, fmt.Sprintf("HH rejected application with status %d", response.StatusCode), nil)
}

func classifyReconciliationFailure(response *http.Response, failure hhErrorResponse) error {
	operation := "applications.reconcile"
	if response.StatusCode == http.StatusTooManyRequests {
		err := operationError(core.ErrorRateLimited, operation, "HH reconciliation was rate limited", nil)
		err.RetryAfter = retryAfter(response.Header.Get("Retry-After"), timeNow())
		return err
	}
	for _, item := range failure.Errors {
		if item.Type == "oauth" {
			return operationError(core.ErrorUnauthorized, operation, "HH credentials are expired, revoked or invalid", nil)
		}
		if item.Type == "captcha_required" || item.Value == "captcha_required" {
			return operationError(core.ErrorConfirmationRequired, operation, "HH requires captcha confirmation", nil)
		}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return operationError(core.ErrorUnauthorized, operation, "HH rejected reconciliation authorization", nil)
	}
	return operationError(core.ErrorTemporaryFailure, operation,
		fmt.Sprintf("HH could not confirm application outcome: status %d", response.StatusCode), nil)
}

var timeNow = func() time.Time { return time.Now().UTC() }
