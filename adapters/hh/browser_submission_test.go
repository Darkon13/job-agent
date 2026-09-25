package hh

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/browser"
	"github.com/Darkon13/job-agent/browser/browsertest"
	"github.com/Darkon13/job-agent/browsercheck"
	"github.com/Darkon13/job-agent/core"
)

func newSubmissionDriver(t *testing.T, fake *browsertest.Fake) *BrowserSubmissionDriver {
	t.Helper()
	driver, err := NewBrowserSubmissionDriver(fake)
	if err != nil {
		t.Fatalf("driver: %v", err)
	}
	driver.stepTimeout = 150 * time.Millisecond
	return driver
}

func TestBrowserSubmissionReturnsCaptchaImage(t *testing.T) {
	fake := browsertest.New()
	fake.ScreenshotData = []byte("captcha-png")
	fake.ContentResult = browser.ContentResult{HTML: `<a data-qa="vacancy-response-link-top">Откликнуться</a>`}
	fake.LocatorFunc = func(_ core.ProfileID, request browser.LocatorRequest) error {
		if request.Action != "wait" {
			return nil
		}
		if request.Selector == vacancyRespondTop || request.Selector == loginCaptchaImage {
			return nil
		}
		return errors.New("not visible")
	}

	outcome, err := newSubmissionDriver(t, fake).Submit(context.Background(), "primary", "137786726", "письмо")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if outcome.State != browsercheck.StateWaitingCaptcha || string(outcome.Image) != "captcha-png" {
		t.Fatalf("outcome=%#v", outcome)
	}
	if calls := fake.CallsOf("goto"); len(calls) != 1 {
		t.Fatalf("goto calls=%d", len(calls))
	}
}

func TestBrowserSubmissionSendsLetterPopup(t *testing.T) {
	fake := browsertest.New()
	fake.ContentResult = browser.ContentResult{HTML: `<a data-qa="vacancy-response-link-top">Откликнуться</a>`}
	var filled string
	var submits int
	fake.LocatorFunc = func(_ core.ProfileID, request browser.LocatorRequest) error {
		switch request.Action {
		case "wait":
			if request.Selector == vacancyRespondTop || request.Selector == responseLetterInput {
				return nil
			}
			return errors.New("not visible")
		case "fill":
			if request.Selector == responseLetterInput {
				filled = request.Value
			}
			return nil
		case "click":
			if request.Selector == responseLetterSubmit {
				submits++
				fake.ContentResult = browser.ContentResult{HTML: `<div>Резюме доставлено</div>`}
			}
			return nil
		default:
			return nil
		}
	}

	outcome, err := newSubmissionDriver(t, fake).Submit(context.Background(), "primary", "137786726", "письмо")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if outcome.State != browsercheck.StateDone || filled != "письмо" || submits != 1 {
		t.Fatalf("outcome=%#v filled=%q submits=%d", outcome, filled, submits)
	}
}

func TestBrowserSubmissionReviewsUnknownPage(t *testing.T) {
	fake := browsertest.New()
	fake.ScreenshotData = []byte("page-png")
	fake.ContentResult = browser.ContentResult{HTML: `<a data-qa="vacancy-response-link-top">Откликнуться</a>`}
	fake.LocatorFunc = func(_ core.ProfileID, request browser.LocatorRequest) error {
		return errors.New("not visible")
	}

	outcome, err := newSubmissionDriver(t, fake).Submit(context.Background(), "primary", "137786726", "")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if outcome.State != browsercheck.StateReview || string(outcome.Image) != "page-png" || outcome.Message == "" {
		t.Fatalf("outcome=%#v", outcome)
	}
}

func TestBrowserSubmissionAnswerCompletesCaptcha(t *testing.T) {
	fake := browsertest.New()
	fake.ContentResult = browser.ContentResult{HTML: `<a data-qa="vacancy-response-link-top">Откликнуться</a>`}
	var pressed bool
	fake.LocatorFunc = func(_ core.ProfileID, request browser.LocatorRequest) error {
		switch request.Action {
		case "wait":
			if request.Selector == loginCaptchaInput {
				return nil
			}
			return errors.New("not visible")
		case "fill":
			return nil
		case "press":
			pressed = true
			fake.ContentResult = browser.ContentResult{HTML: `<div>Резюме доставлено</div>`}
			return nil
		default:
			return nil
		}
	}

	outcome, err := newSubmissionDriver(t, fake).Answer(context.Background(), "primary", "42", "письмо")
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if outcome.State != browsercheck.StateDone || !pressed {
		t.Fatalf("outcome=%#v pressed=%v", outcome, pressed)
	}
}

func TestBrowserSubmissionDetectsAlreadyApplied(t *testing.T) {
	fake := browsertest.New()
	fake.ContentResult = browser.ContentResult{HTML: `<div>Резюме доставлено</div>`}
	fake.LocatorFunc = func(_ core.ProfileID, request browser.LocatorRequest) error {
		return errors.New("not visible")
	}

	outcome, err := newSubmissionDriver(t, fake).Submit(context.Background(), "primary", "137786726", "")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if outcome.State != browsercheck.StateDone {
		t.Fatalf("outcome=%#v", outcome)
	}
	if calls := fake.CallsOf("locator"); len(calls) == 0 {
		t.Fatal("expected locator probes")
	}
}
