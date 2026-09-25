package hh

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/browser"
	"github.com/Darkon13/job-agent/browsercheck"
	"github.com/Darkon13/job-agent/core"
)

// The application browser flow reuses the login captcha selectors: HH renders
// the same Magritte captcha component on the vacancy page.
const (
	vacancyRespondTop    = `[data-qa="vacancy-response-link-top"]`
	vacancyRespondBottom = `[data-qa="vacancy-response-link-bottom"]`
	responseLetterInput  = `textarea[data-qa="vacancy-response-popup-form-letter-input"]`
	responseLetterSubmit = `[data-qa="vacancy-response-letter-submit"]`
	responseAttachLetter = `[data-qa="responded-success-attach-cover-letter"]`
	responseDelivered    = "Резюме доставлено"
	vacancyBaseURL       = "https://hh.ru/vacancy/"

	submissionPollInterval = 500 * time.Millisecond
	submissionProbeTimeout = 400 * time.Millisecond
	submissionStepTimeout  = 15 * time.Second
)

// BrowserSubmissionDriver submits one application through the profile's real
// browser page. HH answers the direct API client with an hhcaptcha document
// while the same action from a browser session is accepted, so an operator
// either solves the shown captcha or the page completes on its own.
type BrowserSubmissionDriver struct {
	client      browser.Client
	sleep       func(context.Context, time.Duration) error
	stepTimeout time.Duration
}

var _ browsercheck.Driver = (*BrowserSubmissionDriver)(nil)

// NewBrowserSubmissionDriver validates the browser dependency.
func NewBrowserSubmissionDriver(client browser.Client) (*BrowserSubmissionDriver, error) {
	if client == nil {
		return nil, errors.New("HH browser submission driver requires a browser client")
	}
	return &BrowserSubmissionDriver{client: client, sleep: sleepContext, stepTimeout: submissionStepTimeout}, nil
}

// Submit opens the vacancy page and performs the ordinary apply flow.
func (driver *BrowserSubmissionDriver) Submit(ctx context.Context, profileID core.ProfileID, vacancyID string, letter string) (browsercheck.Outcome, error) {
	vacancyID = strings.TrimSpace(vacancyID)
	if vacancyID == "" {
		return browsercheck.Outcome{}, &core.OperationError{
			Category: core.ErrorValidationRequired, Operation: "applications.submit.browser_flow", Platform: "hh",
			Message: "vacancy has no external id",
		}
	}
	if _, err := driver.client.Ensure(ctx, profileID, browser.EnsureRequest{}); err != nil {
		return browsercheck.Outcome{}, err
	}
	if _, err := driver.client.Goto(ctx, profileID, browser.GotoRequest{
		URL: vacancyBaseURL + vacancyID, WaitUntil: "domcontentloaded", TimeoutMS: 45_000,
	}); err != nil {
		return browsercheck.Outcome{}, err
	}
	if driver.applied(ctx, profileID) {
		driver.attachLetter(ctx, profileID, letter)
		return browsercheck.Outcome{State: browsercheck.StateDone, Message: "отклик уже существует на странице вакансии"}, nil
	}
	selector, found := driver.waitAnyVisible(ctx, profileID, driver.stepTimeout, vacancyRespondTop, vacancyRespondBottom)
	if !found {
		if driver.applied(ctx, profileID) {
			driver.attachLetter(ctx, profileID, letter)
			return browsercheck.Outcome{State: browsercheck.StateDone, Message: "отклик отправлен из браузера"}, nil
		}
		return driver.review(ctx, profileID, "кнопка отклика не найдена; проверьте вакансию вручную")
	}
	if err := driver.client.Locator(ctx, profileID, browser.LocatorRequest{
		Action: "click", Selector: selector, TimeoutMS: 10_000,
	}); err != nil {
		return browsercheck.Outcome{}, err
	}
	return driver.observe(ctx, profileID, letter)
}

// Answer fills the captcha answer on the still-open vacancy page.
func (driver *BrowserSubmissionDriver) Answer(ctx context.Context, profileID core.ProfileID, answer string, letter string) (browsercheck.Outcome, error) {
	if !driver.visible(ctx, profileID, loginCaptchaInput) {
		return browsercheck.Outcome{}, &core.OperationError{
			Category: core.ErrorConfirmationRequired, Operation: "applications.submit.browser_captcha", Platform: "hh",
			Message: "страница больше не ждёт капчу",
		}
	}
	if err := driver.client.Locator(ctx, profileID, browser.LocatorRequest{
		Action: "fill", Selector: loginCaptchaInput, Value: answer, TimeoutMS: 10_000,
	}); err != nil {
		return browsercheck.Outcome{}, err
	}
	if err := driver.client.Locator(ctx, profileID, browser.LocatorRequest{
		Action: "press", Selector: loginCaptchaInput, Value: "Enter", TimeoutMS: 10_000,
	}); err != nil {
		_ = driver.client.Locator(ctx, profileID, browser.LocatorRequest{
			Action: "click", Selector: loginSubmitButton, TimeoutMS: 10_000,
		})
	}
	return driver.observe(ctx, profileID, letter)
}

// observe polls the page until the platform needs an answer, finishes the
// submission, or stops in a state an operator must inspect.
func (driver *BrowserSubmissionDriver) observe(ctx context.Context, profileID core.ProfileID, letter string) (browsercheck.Outcome, error) {
	deadline := time.Now().Add(driver.stepTimeout)
	letterSent := false
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return browsercheck.Outcome{}, err
		}
		if driver.visible(ctx, profileID, loginCaptchaImage) {
			screenshot, err := driver.client.Screenshot(ctx, profileID, browser.ScreenshotRequest{
				Selector: loginCaptchaImage, TimeoutMS: 10_000,
			})
			if err != nil {
				return browsercheck.Outcome{}, err
			}
			return browsercheck.Outcome{
				State: browsercheck.StateWaitingCaptcha, Image: screenshot,
				Message: "HH просит ввести символы с картинки",
			}, nil
		}
		if !letterSent && driver.visible(ctx, profileID, responseLetterInput) {
			letterSent = true
			if strings.TrimSpace(letter) != "" {
				_ = driver.client.Locator(ctx, profileID, browser.LocatorRequest{
					Action: "fill", Selector: responseLetterInput, Value: letter, TimeoutMS: 10_000,
				})
			}
			if err := driver.client.Locator(ctx, profileID, browser.LocatorRequest{
				Action: "click", Selector: responseLetterSubmit, TimeoutMS: 10_000,
			}); err != nil {
				return browsercheck.Outcome{}, err
			}
			continue
		}
		if driver.applied(ctx, profileID) {
			driver.attachLetter(ctx, profileID, letter)
			return browsercheck.Outcome{State: browsercheck.StateDone, Message: "отклик отправлен из браузера"}, nil
		}
		if err := driver.sleep(ctx, submissionPollInterval); err != nil {
			return browsercheck.Outcome{}, err
		}
	}
	return driver.review(ctx, profileID, "браузер не завершил отклик за отведённое время")
}

// attachLetter attaches the prepared cover letter after a quick apply that
// sent the resume without one. Failures stay silent: the application itself
// is already submitted.
func (driver *BrowserSubmissionDriver) attachLetter(ctx context.Context, profileID core.ProfileID, letter string) {
	letter = strings.TrimSpace(letter)
	if letter == "" || !driver.visible(ctx, profileID, responseAttachLetter) {
		return
	}
	if err := driver.client.Locator(ctx, profileID, browser.LocatorRequest{
		Action: "click", Selector: responseAttachLetter, TimeoutMS: 8_000,
	}); err != nil {
		return
	}
	if err := driver.client.Locator(ctx, profileID, browser.LocatorRequest{
		Action: "fill", Selector: responseLetterInput, Value: letter, TimeoutMS: 10_000,
	}); err != nil {
		return
	}
	_ = driver.client.Locator(ctx, profileID, browser.LocatorRequest{
		Action: "click", Selector: responseLetterSubmit, TimeoutMS: 10_000,
	})
}

// applied reports whether the page shows the vacancy as already answered.
func (driver *BrowserSubmissionDriver) applied(ctx context.Context, profileID core.ProfileID) bool {
	if driver.visible(ctx, profileID, responseAttachLetter) {
		return true
	}
	content, err := driver.content(ctx, profileID)
	if err != nil {
		return false
	}
	return strings.Contains(content, responseDelivered)
}

// review captures the current page for the operator.
func (driver *BrowserSubmissionDriver) review(ctx context.Context, profileID core.ProfileID, message string) (browsercheck.Outcome, error) {
	screenshot, err := driver.client.Screenshot(ctx, profileID, browser.ScreenshotRequest{TimeoutMS: 15_000})
	if err != nil {
		return browsercheck.Outcome{}, err
	}
	return browsercheck.Outcome{State: browsercheck.StateReview, Image: screenshot, Message: message}, nil
}

// waitAnyVisible returns the first selector that becomes visible in time.
func (driver *BrowserSubmissionDriver) waitAnyVisible(ctx context.Context, profileID core.ProfileID, timeout time.Duration, selectors ...string) (string, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, selector := range selectors {
			if driver.visible(ctx, profileID, selector) {
				return selector, true
			}
		}
		if err := driver.sleep(ctx, submissionPollInterval); err != nil {
			return "", false
		}
	}
	return "", false
}

func (driver *BrowserSubmissionDriver) content(ctx context.Context, profileID core.ProfileID) (string, error) {
	result, err := driver.client.Content(ctx, profileID, browser.ContentRequest{TimeoutMS: 15_000})
	if err != nil {
		return "", err
	}
	return result.HTML, nil
}

func (driver *BrowserSubmissionDriver) visible(ctx context.Context, profileID core.ProfileID, selector string) bool {
	err := driver.client.Locator(ctx, profileID, browser.LocatorRequest{
		Action: "wait", Selector: selector, State: "visible", TimeoutMS: int(submissionProbeTimeout / time.Millisecond),
	})
	return err == nil
}
