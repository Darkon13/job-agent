package hh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/adapters/hhapi"
	"github.com/Darkon13/job-agent/browser"
	"github.com/Darkon13/job-agent/core"
)

// mobileApplicationClient is the application transport used when the profile
// has the HH Android app token. hh-applicant-tool sends applications through
// the mobile API directly: it does not read the web response popup, does not
// call suitable_resumes, and does not rewrite the resume before every submit.
type mobileApplicationClient struct {
	api      *hhapi.Client
	worker   *browser.HTTPClient
	answerer func(context.Context, string, string) (string, error)
}

func newMobileApplicationClient(api *hhapi.Client, worker *browser.HTTPClient, answerer func(context.Context, string, string) (string, error)) *mobileApplicationClient {
	return &mobileApplicationClient{api: api, worker: worker, answerer: answerer}
}

var _ adapter.ApplicationTransport = (*mobileApplicationClient)(nil)

func (client *mobileApplicationClient) answerQuestionnaire(ctx context.Context, command adapter.ApplicationSubmitCommand) (adapter.ApplicationSubmitResult, bool) {
	if client.worker == nil {
		return adapter.ApplicationSubmitResult{}, false
	}
	target := "https://hh.ru/vacancy/" + url.PathEscape(command.Vacancy.ExternalID)
	form, err := client.worker.Form(ctx, command.ProfileID, target)
	if err != nil {
		return adapter.ApplicationSubmitResult{}, false
	}
	var answers []browser.FormAnswer
	for _, field := range form.Fields {
		if field.Name == "" || (field.Tag != "textarea" && field.Type != "text") {
			continue
		}
		question := strings.TrimSpace(field.Context + " " + field.Label)
		text := strings.ToLower(question)
		value := ""
		if strings.Contains(text, "ожидан") || strings.Contains(text, "зарплат") {
			low, high, salaryErr := client.api.SalaryRange(ctx, command.Vacancy.ExternalID)
			if salaryErr != nil {
				return adapter.ApplicationSubmitResult{}, false
			}
			value = fmt.Sprintf("Ожидания: %d–%d ₽, торг уместен.", low, high)
		} else if client.answerer != nil {
			generated, genErr := client.answerer(ctx, command.Vacancy.ExternalID, question)
			if genErr != nil {
				return adapter.ApplicationSubmitResult{}, false
			}
			value = generated
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		answers = append(answers, browser.FormAnswer{Selector: fmt.Sprintf(`[name="%s"]`, field.Name), Value: value})
	}
	if len(answers) == 0 {
		return adapter.ApplicationSubmitResult{}, false
	}
	result, err := client.worker.Answers(ctx, command.ProfileID, answers, true)
	if err != nil {
		return adapter.ApplicationSubmitResult{}, false
	}
	id := ""
	if result.Confirmed {
		id = client.api.NegotiationID(ctx, command.Vacancy.ExternalID)
	}
	if id == "" {
		id = client.api.NegotiationID(ctx, command.Vacancy.ExternalID)
	}
	if id != "" {
		return adapter.ApplicationSubmitResult{ExternalNegotiationID: id, Applied: true}, true
	}
	return adapter.ApplicationSubmitResult{}, false
}

func (client *mobileApplicationClient) SubmitApplication(ctx context.Context, command adapter.ApplicationSubmitCommand) (adapter.ApplicationSubmitResult, error) {
	if client == nil || client.api == nil {
		return adapter.ApplicationSubmitResult{}, operationError(core.ErrorUnsupported, "applications.submit.hhapi", "HH mobile API is not configured", nil)
	}
	if err := validateApplicationCommand(command.ProfileID, command); err != nil {
		return adapter.ApplicationSubmitResult{}, err
	}
	if retryAt, pending := captchaPending(); pending {
		return adapter.ApplicationSubmitResult{}, &core.OperationError{
			Category: core.ErrorRateLimited, Operation: "applications.submit.hhapi",
			Platform: command.Vacancy.Platform, RetryAfter: &retryAt,
			Message: "HH captcha is pending; solve it before submitting more applications",
		}
	}
	vacancy, vacErr := client.api.Vacancy(ctx, command.Vacancy.ExternalID)
	if vacErr != nil {
		retryAt := time.Now().Add(10 * time.Minute)
		return adapter.ApplicationSubmitResult{}, &core.OperationError{
			Category: core.ErrorTemporaryFailure, Operation: "applications.submit.hhapi",
			Platform: command.Vacancy.Platform, RetryAfter: &retryAt,
			Message: "cannot load vacancy before applying: " + vacErr.Error(),
		}
	}
	if vacancy.BlockedRole() {
		return adapter.ApplicationSubmitResult{}, browserReviewError(core.ErrorValidationRequired, "role_blocked",
			"HH vacancy is outside the target profile")
	}
	if vacancy.BlockedCompany() {
		return adapter.ApplicationSubmitResult{}, browserReviewError(core.ErrorValidationRequired, "company_blocked",
			"HH employer is in the blacklist")
	}
	if !vacancy.Remote() {
		return adapter.ApplicationSubmitResult{}, browserReviewError(core.ErrorValidationRequired, "not_remote",
			"HH vacancy does not allow remote work")
	}
	result, err := client.api.Apply(ctx, hhapi.ApplyRequest{
		VacancyID: command.Vacancy.ExternalID,
		ResumeID:  command.ResumeID,
		Message:   command.Message,
	})
	if err == nil {
		return adapter.ApplicationSubmitResult{ExternalNegotiationID: result.ID, Applied: true}, nil
	}
	return client.classifyMobileApplicationError(command, err)
}

func (client *mobileApplicationClient) classifyMobileApplicationError(command adapter.ApplicationSubmitCommand, err error) (adapter.ApplicationSubmitResult, error) {
	var captcha *hhapi.CaptchaRequiredError
	if errors.As(err, &captcha) {
		if captcha.URL != "" {
			_ = os.WriteFile(captchaPendingFilePath, []byte(captcha.URL), 0o644)
		}
		retryAt := time.Now().Add(10 * time.Minute)
		return adapter.ApplicationSubmitResult{}, &core.OperationError{
			Category: core.ErrorRateLimited, Operation: "applications.submit.hhapi",
			Platform: command.Vacancy.Platform, RetryAfter: &retryAt,
			Message: "HH requires captcha: " + captcha.Error(),
		}
	}
	var apiError *hhapi.APIError
	if errors.As(err, &apiError) {
		body := strings.ToLower(apiError.Body)
		switch {
		case strings.Contains(body, "already_applied"):
			return adapter.ApplicationSubmitResult{AlreadyApplied: true, Applied: true}, nil
		case strings.Contains(body, "limit_exceeded") || strings.Contains(body, "daily negotiations limit") || strings.Contains(body, "negotiations-limit-exceeded"):
			retryAt := apiError.RetryAfter
			if retryAt.IsZero() {
				retryAt = time.Now().Add(24 * time.Hour)
			}
			return adapter.ApplicationSubmitResult{}, &core.OperationError{
				Category: core.ErrorQuotaExceeded, Operation: "applications.submit.hhapi",
				Platform: command.Vacancy.Platform, RetryAfter: &retryAt,
				Message: "HH daily negotiations limit reached",
			}
		case strings.Contains(body, "test_required") || strings.Contains(body, "questionnaire") || strings.Contains(body, "must process test"):
			if result, handled := client.answerQuestionnaire(context.Background(), command); handled {
				return result, nil
			}
			return adapter.ApplicationSubmitResult{}, browserReviewErrorAt(
				"applications.submit.hhapi", core.ErrorValidationRequired, "questionnaire_required",
				"HH vacancy requires a test or questionnaire",
			)
		case strings.Contains(body, "resume_not_found") || strings.Contains(body, "resume_visibility_conflict") ||
			strings.Contains(body, "empty_message") || strings.Contains(body, "too_long_message") || strings.Contains(body, "invalid_resume"):
			return adapter.ApplicationSubmitResult{}, browserReviewErrorAt(
				"applications.submit.hhapi", core.ErrorValidationRequired, "resume_not_suitable",
				"HH rejected the configured resume or application input",
			)
		case strings.Contains(body, "vacancy_not_found") || strings.Contains(body, "invalid_vacancy") ||
			strings.Contains(body, "application_denied") || strings.Contains(body, "archived"):
			return adapter.ApplicationSubmitResult{}, browserReviewErrorAt(
				"applications.submit.hhapi", core.ErrorValidationRequired, "vacancy_closed",
				"HH vacancy is unavailable for an application",
			)
		}
		switch {
		case apiError.StatusCode == http.StatusTooManyRequests:
			retryAt := apiError.RetryAfter
			if retryAt.IsZero() {
				retryAt = time.Now().Add(10 * time.Minute)
			}
			return adapter.ApplicationSubmitResult{}, &core.OperationError{
				Category: core.ErrorRateLimited, Operation: "applications.submit.hhapi",
				Platform: command.Vacancy.Platform, RetryAfter: &retryAt,
				Message: "HH application request was rate limited",
			}
		case apiError.StatusCode == http.StatusUnauthorized || apiError.StatusCode == http.StatusForbidden:
			return adapter.ApplicationSubmitResult{}, &core.OperationError{
				Category: core.ErrorUnauthorized, Operation: "applications.submit.hhapi",
				Platform: command.Vacancy.Platform,
				Message:  "HH mobile session was rejected",
			}
		case apiError.StatusCode >= 300 && apiError.StatusCode < 400:
			return adapter.ApplicationSubmitResult{}, browserReviewErrorAt(
				"applications.submit.hhapi", core.ErrorValidationRequired, "questionnaire_required",
				"HH redirected the application to a form or questionnaire",
			)
		case apiError.StatusCode >= 500:
			return adapter.ApplicationSubmitResult{}, &core.OperationError{
				Category: core.ErrorAmbiguousResult, Operation: "applications.submit.hhapi",
				Platform: command.Vacancy.Platform,
				Message:  "HH application outcome is unknown after a server error",
			}
		case apiError.StatusCode == http.StatusBadRequest || apiError.StatusCode == http.StatusUnprocessableEntity:
			return adapter.ApplicationSubmitResult{}, &core.OperationError{
				Category: core.ErrorValidationRequired, Operation: "applications.submit.hhapi",
				Platform: command.Vacancy.Platform,
				Message:  "HH rejected application input",
			}
		}
		return adapter.ApplicationSubmitResult{}, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "applications.submit.hhapi",
			Platform: command.Vacancy.Platform, Message: apiError.Error(),
		}
	}
	if strings.Contains(err.Error(), "captcha attempts exhausted") || strings.Contains(err.Error(), "captcha:") {
		retryAt := time.Now().Add(10 * time.Minute)
		return adapter.ApplicationSubmitResult{}, &core.OperationError{
			Category: core.ErrorRateLimited, Operation: "applications.submit.hhapi",
			Platform: command.Vacancy.Platform, RetryAfter: &retryAt,
			Message: "HH captcha was not accepted",
		}
	}
	return adapter.ApplicationSubmitResult{}, &core.OperationError{
		Category: core.ErrorAmbiguousResult, Operation: "applications.submit.hhapi",
		Platform: command.Vacancy.Platform,
		Message:  "HH mobile application outcome is unknown",
		Cause:    err,
	}
}
