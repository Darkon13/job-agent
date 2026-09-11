package hh

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/auth"
	"github.com/Darkon13/job-agent/browser"
	"github.com/Darkon13/job-agent/browser/browsertest"
	"github.com/Darkon13/job-agent/core"
)

func loginDriverFixture(t *testing.T, fake *browsertest.Fake) *LoginDriver {
	t.Helper()
	driver, err := NewLoginDriver(fake, []LoginSettings{{ProfileID: "primary", StateFile: "/tmp/state.json"}})
	if err != nil {
		t.Fatalf("new login driver: %v", err)
	}
	driver.sleep = func(context.Context, time.Duration) error { return nil }
	return driver
}

func loginSession(t *testing.T, status core.AuthSessionStatus) core.AuthSession {
	t.Helper()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	session, err := core.NewAuthSession(core.NewAuthSessionParams{
		ID: "auth-1", Platform: "hh", ProfileID: "primary",
		BrowserStateReference: "/tmp/state.json", ExpiresAt: now.Add(15 * time.Minute),
	}, now)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	switch status {
	case core.AuthSessionWaitingIdentifier:
		if err := session.BeginIdentifier(now.Add(time.Second)); err != nil {
			t.Fatalf("begin identifier: %v", err)
		}
	case core.AuthSessionWaitingOTP:
		if err := session.BeginIdentifier(now.Add(time.Second)); err != nil {
			t.Fatalf("begin identifier: %v", err)
		}
		if err := session.RequireChallenge(core.AuthChallenge{
			ID: "challenge-1", Kind: core.AuthChallengeOTP, Deadline: now.Add(time.Minute),
		}, now.Add(2*time.Second)); err != nil {
			t.Fatalf("require challenge: %v", err)
		}
	case core.AuthSessionWaitingPassword:
		if err := session.BeginIdentifier(now.Add(time.Second)); err != nil {
			t.Fatalf("begin identifier: %v", err)
		}
		if err := session.RequireChallenge(core.AuthChallenge{
			ID: "challenge-1", Kind: core.AuthChallengePassword, Deadline: now.Add(time.Minute),
		}, now.Add(2*time.Second)); err != nil {
			t.Fatalf("require challenge: %v", err)
		}
	case core.AuthSessionWaitingCaptcha:
		if err := session.BeginIdentifier(now.Add(time.Second)); err != nil {
			t.Fatalf("begin identifier: %v", err)
		}
		if err := session.RequireChallenge(core.AuthChallenge{
			ID: "challenge-1", Kind: core.AuthChallengeCaptcha, MediaType: "image/png", Deadline: now.Add(time.Minute),
		}, now.Add(2*time.Second)); err != nil {
			t.Fatalf("require challenge: %v", err)
		}
	}
	return session
}

func invisibleLoginElements(profileID core.ProfileID, request browser.LocatorRequest) error {
	switch request.Selector {
	case loginCaptchaImage, loginPasswordExpand, loginOTPInput:
		return errors.New("element is not visible")
	default:
		return nil
	}
}

func TestLoginDriverStartRequiresConfiguredProfile(t *testing.T) {
	driver, err := NewLoginDriver(browsertest.New(), nil)
	if err != nil {
		t.Fatalf("new driver: %v", err)
	}
	_, err = driver.Start(context.Background(), "primary")
	var operationError *core.OperationError
	if !errors.As(err, &operationError) || operationError.Category != core.ErrorUnsupported {
		t.Fatalf("error = %#v, want unsupported", err)
	}
}

func TestLoginDriverStartOpensLoginPageAndAsksIdentifier(t *testing.T) {
	fake := browsertest.New()
	driver := loginDriverFixture(t, fake)
	outcome, err := driver.Start(context.Background(), "primary")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if outcome.Request == nil || !outcome.Request.Identifier {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(fake.CallsOf("ensure")) != 1 || len(fake.CallsOf("goto")) != 1 {
		t.Fatalf("calls = %#v", fake.Calls)
	}
}

func TestLoginDriverIdentifierLeadsToOTPChallenge(t *testing.T) {
	fake := browsertest.New()
	fake.PageResult = browser.PageInfo{URL: defaultLoginURL, HasPage: true}
	fake.LocatorFunc = func(_ core.ProfileID, request browser.LocatorRequest) error {
		switch request.Selector {
		case loginOTPInput:
			return nil
		case loginCaptchaImage, loginPasswordExpand:
			return errors.New("element is not visible")
		default:
			return nil
		}
	}
	driver := loginDriverFixture(t, fake)
	outcome, err := driver.Continue(context.Background(), loginSession(t, core.AuthSessionWaitingIdentifier), auth.Input{
		Kind: auth.InputIdentifier, Value: "user@example.com",
	})
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if outcome.Request == nil || outcome.Request.Challenge == nil || outcome.Request.Challenge.Kind != core.AuthChallengeOTP {
		t.Fatalf("outcome = %#v", outcome)
	}
	var filled, submitted bool
	for _, call := range fake.CallsOf("locator") {
		request := call.Request.(browser.LocatorRequest)
		if request.Selector == loginEmailInput && request.Action == "fill" && request.Value == "user@example.com" {
			filled = true
		}
		if request.Selector == loginSubmitButton && request.Action == "click" {
			submitted = true
		}
	}
	if !filled || !submitted {
		t.Fatalf("email step not driven: filled=%v submitted=%v", filled, submitted)
	}
}

func TestLoginDriverCaptchaReturnsScreenshotChallenge(t *testing.T) {
	fake := browsertest.New()
	fake.PageResult = browser.PageInfo{URL: defaultLoginURL, HasPage: true}
	fake.ScreenshotData = []byte{0x89, 'P', 'N', 'G'}
	fake.LocatorFunc = func(_ core.ProfileID, request browser.LocatorRequest) error {
		if request.Selector == loginCaptchaImage {
			return nil
		}
		return errors.New("not visible")
	}
	driver := loginDriverFixture(t, fake)
	outcome, err := driver.Continue(context.Background(), loginSession(t, core.AuthSessionWaitingIdentifier), auth.Input{
		Kind: auth.InputIdentifier, Value: "user@example.com",
	})
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	challenge := outcome.Request.Challenge
	if challenge == nil || challenge.Kind != core.AuthChallengeCaptcha || challenge.MediaType != "image/png" ||
		string(challenge.Payload) != string(fake.ScreenshotData) {
		t.Fatalf("challenge = %#v", challenge)
	}
}

func TestLoginDriverOTPCompletesWithSanitizedBrowserState(t *testing.T) {
	fake := browsertest.New()
	fake.LocatorFunc = func(_ core.ProfileID, request browser.LocatorRequest) error {
		switch request.Selector {
		case loginOTPInput:
			if request.Action == "fill" || request.Action == "press" {
				return nil
			}
			return errors.New("element is not visible")
		case loginCaptchaImage, loginPasswordExpand:
			return errors.New("element is not visible")
		default:
			return nil
		}
	}
	fake.StorageState = json.RawMessage(`{"cookies":[
		{"name":"hhtoken","domain":".hh.ru","value":"secret"},
		{"name":"foreign","domain":".example.com","value":"drop"}
	],"origins":[{"origin":"https://hh.ru","localStorage":[]},{"origin":"https://example.com","localStorage":[]}]}`)
	pageCalls := 0
	fake.PageFunc = func(core.ProfileID) (browser.PageInfo, error) {
		pageCalls++
		if pageCalls == 1 {
			return browser.PageInfo{URL: defaultLoginURL, HasPage: true}, nil
		}
		return browser.PageInfo{URL: "https://hh.ru/", HasPage: true}, nil
	}
	driver := loginDriverFixture(t, fake)
	outcome, err := driver.Continue(context.Background(), loginSession(t, core.AuthSessionWaitingOTP), auth.Input{
		Kind: auth.InputOTP, Value: "1234",
	})
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if outcome.BrowserState == nil {
		t.Fatalf("outcome = %#v", outcome)
	}
	var state struct {
		Cookies []struct {
			Domain string `json:"domain"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(outcome.BrowserState.Data, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if len(state.Cookies) != 1 || state.Cookies[0].Domain != ".hh.ru" {
		t.Fatalf("sanitized cookies = %#v", state.Cookies)
	}
}

func TestLoginDriverPasswordBranchIsUnsupported(t *testing.T) {
	fake := browsertest.New()
	fake.PageResult = browser.PageInfo{URL: defaultLoginURL, HasPage: true}
	fake.LocatorFunc = func(_ core.ProfileID, request browser.LocatorRequest) error {
		if request.Selector == loginPasswordExpand {
			return nil
		}
		return errors.New("not visible")
	}
	driver := loginDriverFixture(t, fake)
	_, err := driver.Continue(context.Background(), loginSession(t, core.AuthSessionWaitingIdentifier), auth.Input{
		Kind: auth.InputIdentifier, Value: "user@example.com",
	})
	var operationError *core.OperationError
	if !errors.As(err, &operationError) || operationError.Category != core.ErrorUnsupported {
		t.Fatalf("error = %#v, want unsupported", err)
	}
	if _, err := driver.Continue(context.Background(), loginSession(t, core.AuthSessionWaitingPassword), auth.Input{
		Kind: auth.InputPassword, Value: "secret",
	}); err == nil {
		t.Fatal("expected password continuation to be unsupported")
	}
}
