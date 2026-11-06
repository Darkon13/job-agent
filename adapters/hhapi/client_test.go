package hhapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func newClientFixture(t *testing.T, handler roundTripFunc) *Client {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HH_COOKIE_FILE", filepath.Join(dir, "cookies.txt"))
	tokenFile := filepath.Join(dir, "token.json")
	if err := os.WriteFile(tokenFile, []byte(`{"access_token":"USER-test","refresh_token":"refresh","access_expires_at":4102444800}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	client.SetDelay(0)
	client.http.Transport = handler
	return client
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestRequestUsesMobileAppHeaders(t *testing.T) {
	var client *Client
	client = newClientFixture(t, func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-HH-App-Active") != "true" {
			t.Errorf("X-HH-App-Active = %q", request.Header.Get("X-HH-App-Active"))
		}
		if request.Header.Get("User-Agent") != client.userAgent {
			t.Errorf("User-Agent = %q", request.Header.Get("User-Agent"))
		}
		if request.Header.Get("Authorization") != "Bearer USER-test" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		return response(http.StatusOK, `{"id":"42"}`), nil
	})
	var out struct {
		ID string `json:"id"`
	}
	if err := client.Request(context.Background(), http.MethodGet, "/vacancies/42", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "42" {
		t.Fatalf("out = %#v", out)
	}
}

func TestApplyUsesMobileClientAndKeepsCoverLetter(t *testing.T) {
	var body string
	client := newClientFixture(t, func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-HH-App-Active") != "true" {
			t.Errorf("X-HH-App-Active = %q", request.Header.Get("X-HH-App-Active"))
		}
		if request.Header.Get("X-Force-App-Access") != "" {
			t.Errorf("unexpected X-Force-App-Access")
		}
		data, _ := io.ReadAll(request.Body)
		body = string(data)
		return response(http.StatusCreated, `{}`), nil
	})
	if _, err := client.Apply(context.Background(), ApplyRequest{VacancyID: "42", ResumeID: "17", Message: "Здравствуйте"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "vacancy_id=42") || !strings.Contains(body, "resume_id=17") || !strings.Contains(body, "message=") {
		t.Fatalf("application body = %q", body)
	}
}

func TestApplyDoesNotFollowRedirectAsSuccess(t *testing.T) {
	client := newClientFixture(t, func(*http.Request) (*http.Response, error) {
		response := response(http.StatusFound, "")
		response.Header.Set("Location", "https://hh.ru/applicant/vacancy_response")
		return response, nil
	})
	_, err := client.Apply(context.Background(), ApplyRequest{VacancyID: "42", ResumeID: "17"})
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusFound {
		t.Fatalf("error = %#v", err)
	}
}

func TestApplyReturnsTypedAPIError(t *testing.T) {
	client := newClientFixture(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusForbidden, `{"errors":[{"type":"negotiations","value":"limit_exceeded"}]}`), nil
	})
	_, err := client.Apply(context.Background(), ApplyRequest{VacancyID: "42", ResumeID: "17"})
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusForbidden || !strings.Contains(apiError.Body, "limit_exceeded") {
		t.Fatalf("error = %#v", err)
	}
}

func TestRequestSolvesCaptchaAndRetries(t *testing.T) {
	var calls atomic.Int32
	client := newClientFixture(t, func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if calls.Load() == 1 {
			return response(http.StatusForbidden, `{"errors":[{"type":"captcha_required","captcha_url":"https://hh.ru/account/captcha?state=x"}]}`), nil
		}
		return response(http.StatusOK, `{"id":"42"}`), nil
	})
	solved := false
	client.SetCaptchaSolver(func(context.Context, string) error {
		solved = true
		return nil
	})
	var out struct {
		ID string `json:"id"`
	}
	if err := client.Request(context.Background(), http.MethodGet, "/vacancies/42", nil, &out); err != nil {
		t.Fatal(err)
	}
	if !solved || calls.Load() != 2 || out.ID != "42" {
		t.Fatalf("solved=%v calls=%d out=%#v", solved, calls.Load(), out)
	}
}

func TestRequestHonoursConfiguredDelay(t *testing.T) {
	var calls atomic.Int32
	client := newClientFixture(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusOK, `{}`), nil
	})
	client.SetDelay(60 * time.Millisecond)
	start := time.Now()
	if err := client.Request(context.Background(), http.MethodGet, "/vacancies/42", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Request(context.Background(), http.MethodGet, "/vacancies/42", nil, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("requests were not delayed: %s", elapsed)
	}
}
