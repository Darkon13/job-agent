package hh

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/auth"
	"github.com/Darkon13/job-agent/browser"
	"github.com/Darkon13/job-agent/core"
)

const (
	defaultLoginURL   = "https://hh.ru/account/login?role=applicant"
	loginStepTimeout  = 20 * time.Second
	loginPollInterval = 500 * time.Millisecond
	elementProbeMS    = 800
)

const (
	loginEmailType      = `[data-qa^="credential-type-EMAIL"]`
	loginEmailInput     = `input[data-qa="applicant-login-input-email"]`
	loginSubmitButton   = `[data-qa="submit-button"]`
	loginOTPInput       = `input[data-qa="magritte-pincode-input-field"]`
	loginCaptchaImage   = `img[data-qa="account-captcha-picture"]`
	loginCaptchaInput   = `input[data-qa="account-captcha-input"]`
	loginPasswordExpand = `[data-qa="expand-login-by-password"]`
)

type LoginSettings struct {
	ProfileID core.ProfileID
	StateFile string
	LoginURL  string
	Headless  *bool
}

// LoginDriver performs the interactive HH browser login through the thin
// browser worker. It supports the applicant e-mail and one-time-code flow plus
// image captcha; the password branch stays a manual decision.
type LoginDriver struct {
	client   browser.Client
	settings map[core.ProfileID]LoginSettings
	sleep    func(context.Context, time.Duration) error
}

var _ auth.Driver = (*LoginDriver)(nil)

func NewLoginDriver(client browser.Client, settings []LoginSettings) (*LoginDriver, error) {
	if client == nil {
		return nil, errors.New("HH login driver requires a browser client")
	}
	configured := make(map[core.ProfileID]LoginSettings, len(settings))
	for _, setting := range settings {
		if setting.ProfileID == "" {
			return nil, errors.New("HH login settings require a profile")
		}
		if _, exists := configured[setting.ProfileID]; exists {
			return nil, fmt.Errorf("duplicate HH login settings for profile %s", setting.ProfileID)
		}
		if strings.TrimSpace(setting.LoginURL) == "" {
			setting.LoginURL = defaultLoginURL
		}
		configured[setting.ProfileID] = setting
	}
	return &LoginDriver{client: client, settings: configured, sleep: sleepContext}, nil
}

func (driver *LoginDriver) Start(ctx context.Context, profileID core.ProfileID) (auth.Outcome, error) {
	setting, ok := driver.settings[profileID]
	if !ok {
		return auth.Outcome{}, &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "auth.login.start",
			Message: "browser login is not configured for this profile",
		}
	}
	if _, err := driver.client.Ensure(ctx, profileID, browser.EnsureRequest{Headless: setting.Headless}); err != nil {
		return auth.Outcome{}, err
	}
	if _, err := driver.client.Goto(ctx, profileID, browser.GotoRequest{
		URL: setting.LoginURL, WaitUntil: "domcontentloaded", TimeoutMS: 30_000,
	}); err != nil {
		return auth.Outcome{}, err
	}
	return auth.Outcome{Request: &auth.Request{Identifier: true}}, nil
}

func (driver *LoginDriver) Continue(ctx context.Context, session core.AuthSession, input auth.Input) (auth.Outcome, error) {
	setting, ok := driver.settings[session.ProfileID]
	if !ok {
		return auth.Outcome{}, &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "auth.login.step",
			Message: "browser login is not configured for this profile",
		}
	}
	switch session.Status {
	case core.AuthSessionWaitingIdentifier:
		if driver.authenticated(ctx, setting) {
			return driver.exportState(ctx, setting)
		}
		if outcome, found, err := driver.challenge(ctx, setting); found || err != nil {
			return outcome, err
		}
		// The login page may start on the phone credential; prefer e-mail when
		// the switch is present. Failure is ignored because the page may
		// already show the e-mail form.
		_ = driver.client.Locator(ctx, setting.ProfileID, browser.LocatorRequest{
			Action: "click", Selector: loginEmailType, TimeoutMS: 2_000,
		})
		if err := driver.client.Locator(ctx, setting.ProfileID, browser.LocatorRequest{
			Action: "fill", Selector: loginEmailInput, Value: input.Value, TimeoutMS: 10_000,
		}); err != nil {
			return auth.Outcome{}, err
		}
		if err := driver.client.Locator(ctx, setting.ProfileID, browser.LocatorRequest{
			Action: "click", Selector: loginSubmitButton, TimeoutMS: 10_000,
		}); err != nil {
			return auth.Outcome{}, err
		}
		return driver.waitNextStep(ctx, setting)
	case core.AuthSessionWaitingOTP:
		if err := driver.client.Locator(ctx, setting.ProfileID, browser.LocatorRequest{
			Action: "fill", Selector: loginOTPInput, Value: input.Value, TimeoutMS: 10_000,
		}); err != nil {
			return auth.Outcome{}, err
		}
		if err := driver.client.Locator(ctx, setting.ProfileID, browser.LocatorRequest{
			Action: "press", Selector: loginOTPInput, Value: "Enter", TimeoutMS: 10_000,
		}); err != nil {
			return auth.Outcome{}, err
		}
		return driver.waitNextStep(ctx, setting)
	case core.AuthSessionWaitingCaptcha:
		if err := driver.client.Locator(ctx, setting.ProfileID, browser.LocatorRequest{
			Action: "fill", Selector: loginCaptchaInput, Value: input.Value, TimeoutMS: 10_000,
		}); err != nil {
			return auth.Outcome{}, err
		}
		if err := driver.client.Locator(ctx, setting.ProfileID, browser.LocatorRequest{
			Action: "click", Selector: loginSubmitButton, TimeoutMS: 10_000,
		}); err != nil {
			return auth.Outcome{}, err
		}
		return driver.waitNextStep(ctx, setting)
	case core.AuthSessionWaitingPassword:
		return auth.Outcome{}, &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "auth.login.password",
			Message: "password login is not supported; use the one-time code",
		}
	default:
		return auth.Outcome{}, fmt.Errorf("cannot continue HH browser login in status %q", session.Status)
	}
}

func (driver *LoginDriver) challenge(ctx context.Context, setting LoginSettings) (auth.Outcome, bool, error) {
	if driver.visible(ctx, setting.ProfileID, loginCaptchaImage) {
		outcome, err := driver.captchaOutcome(ctx, setting)
		return outcome, true, err
	}
	if driver.visible(ctx, setting.ProfileID, loginPasswordExpand) {
		return auth.Outcome{}, true, &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "auth.login.password",
			Message: "HH asked for a password; password login is not supported",
		}
	}
	return auth.Outcome{}, false, nil
}

func (driver *LoginDriver) waitNextStep(ctx context.Context, setting LoginSettings) (auth.Outcome, error) {
	deadline := time.Now().Add(loginStepTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return auth.Outcome{}, err
		}
		if driver.authenticated(ctx, setting) {
			return driver.exportState(ctx, setting)
		}
		if outcome, found, err := driver.challenge(ctx, setting); found || err != nil {
			return outcome, err
		}
		if driver.visible(ctx, setting.ProfileID, loginOTPInput) {
			return auth.Outcome{Request: &auth.Request{Challenge: &auth.Challenge{
				Kind: core.AuthChallengeOTP, Prompt: "Введите код из письма или SMS",
			}}}, nil
		}
		if err := driver.sleep(ctx, loginPollInterval); err != nil {
			return auth.Outcome{}, err
		}
	}
	return auth.Outcome{}, &core.OperationError{
		Category: core.ErrorTemporaryFailure, Operation: "auth.login.wait",
		Message: "HH login step did not complete in time",
	}
}

func (driver *LoginDriver) exportState(ctx context.Context, setting LoginSettings) (auth.Outcome, error) {
	raw, err := driver.client.ExportStorageState(ctx, setting.ProfileID)
	if err != nil {
		return auth.Outcome{}, err
	}
	sanitized, _, err := SanitizeBrowserStorageStateData(raw)
	if err != nil {
		return auth.Outcome{}, &core.OperationError{
			Category: core.ErrorUnauthorized, Operation: "auth.login.state",
			Message: "browser session does not contain HH cookies", Cause: err,
		}
	}
	return auth.Outcome{BrowserState: &auth.BrowserState{Data: sanitized}}, nil
}

func (driver *LoginDriver) captchaOutcome(ctx context.Context, setting LoginSettings) (auth.Outcome, error) {
	screenshot, err := driver.client.Screenshot(ctx, setting.ProfileID, browser.ScreenshotRequest{
		Selector: loginCaptchaImage, TimeoutMS: 10_000,
	})
	if err != nil {
		return auth.Outcome{}, err
	}
	return auth.Outcome{Request: &auth.Request{Challenge: &auth.Challenge{
		Kind: core.AuthChallengeCaptcha, MediaType: "image/png",
		Prompt: "Введите символы с картинки", Payload: screenshot,
	}}}, nil
}

func (driver *LoginDriver) authenticated(ctx context.Context, setting LoginSettings) bool {
	info, err := driver.client.Page(ctx, setting.ProfileID)
	if err != nil || !info.HasPage || strings.TrimSpace(info.URL) == "" {
		return false
	}
	url := strings.ToLower(info.URL)
	return !strings.Contains(url, "/account/login") && !strings.Contains(url, "login")
}

func (driver *LoginDriver) visible(ctx context.Context, profileID core.ProfileID, selector string) bool {
	err := driver.client.Locator(ctx, profileID, browser.LocatorRequest{
		Action: "wait", Selector: selector, State: "visible", TimeoutMS: elementProbeMS,
	})
	return err == nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
