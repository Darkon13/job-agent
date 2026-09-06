package hh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const (
	defaultProfileURL = "https://hh.ru/applicant/profile/me"
	defaultTouchURL   = "https://resume-profile-front.hh.ru/profile/shards/resume/touch"
)

var initialStatePattern = regexp.MustCompile(`(?s)<template[^>]*ResumeProfileFront-InitialState[^>]*>(.*?)</template>`)

type ResumeTouchTransport struct {
	stateFile  string
	profileURL string
	touchURL   string
	client     *http.Client
}

type ResumeTouchProbe struct {
	CanTouch    bool
	NextTouchAt *time.Time
}

type browserStorageState struct {
	Cookies []browserCookie `json:"cookies"`
}

type browserCookie struct {
	Name, Value, Domain, Path string
	Expires                   float64
	HTTPOnly, Secure          bool
	SameSite                  string
}

type resumeTouchState struct {
	ID            string `json:"id"`
	Hash          string `json:"hash"`
	CanTouch      bool   `json:"canTouch"`
	NextTouchAtMS int64  `json:"nextTouchAt"`
	UpdateTimeout int64  `json:"update_timeout"`
}

func NewResumeTouchTransport(stateFile string, client *http.Client) (*ResumeTouchTransport, error) {
	if strings.TrimSpace(stateFile) == "" {
		return nil, errors.New("HH resume touch transport requires browser state file")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &ResumeTouchTransport{stateFile: stateFile, profileURL: defaultProfileURL, touchURL: defaultTouchURL, client: client}, nil
}

// ProbeResume verifies the browser session and configured resume using only a
// GET request. It is safe for operator preflight and never touches the resume.
func (transport *ResumeTouchTransport) ProbeResume(ctx context.Context, resumeID string) (ResumeTouchProbe, error) {
	if strings.TrimSpace(resumeID) == "" {
		return ResumeTouchProbe{}, errors.New("HH resume probe requires resume")
	}
	client, err := transport.authenticatedClient()
	if err != nil {
		return ResumeTouchProbe{}, err
	}
	state, err := transport.readTouchState(ctx, client, resumeID)
	if err != nil {
		return ResumeTouchProbe{}, err
	}
	return ResumeTouchProbe{CanTouch: state.CanTouch, NextTouchAt: millisTime(state.NextTouchAtMS)}, nil
}

func (transport *ResumeTouchTransport) TouchResume(ctx context.Context, command adapter.ResumeTouchCommand) (adapter.ResumeTouchResult, error) {
	if command.ProfileID == "" || strings.TrimSpace(command.ResumeID) == "" {
		return adapter.ResumeTouchResult{}, errors.New("HH resume touch requires profile and resume")
	}
	client, err := transport.authenticatedClient()
	if err != nil {
		return adapter.ResumeTouchResult{}, err
	}
	state, err := transport.readTouchState(ctx, client, command.ResumeID)
	if err != nil {
		return adapter.ResumeTouchResult{}, err
	}
	next := millisTime(state.NextTouchAtMS)
	if !state.CanTouch {
		return adapter.ResumeTouchResult{NextTouchAt: next}, touchRateLimit(next)
	}
	body, _ := json.Marshal(map[string]string{"hash": state.Hash})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, transport.touchURL, bytes.NewReader(body))
	if err != nil {
		return adapter.ResumeTouchResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	request.Header.Set("Referer", transport.profileURL)
	if xsrf := cookieValue(client, request.URL, "_xsrf"); xsrf != "" {
		request.Header.Set("X-Xsrftoken", xsrf)
	}
	response, err := client.Do(request)
	if err != nil {
		return adapter.ResumeTouchResult{}, &core.OperationError{Category: core.ErrorTemporaryFailure, Operation: "resumes.touch", Platform: Name, Message: "touch request failed", Cause: err}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return adapter.ResumeTouchResult{}, nil
	}
	if response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusTooManyRequests {
		if refreshed, refreshErr := transport.readTouchState(ctx, client, command.ResumeID); refreshErr == nil {
			next = millisTime(refreshed.NextTouchAtMS)
		}
		return adapter.ResumeTouchResult{NextTouchAt: next}, touchRateLimit(next)
	}
	category := core.ErrorPermanentFailure
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		category = core.ErrorUnauthorized
	} else if response.StatusCode >= 500 {
		category = core.ErrorTemporaryFailure
	}
	return adapter.ResumeTouchResult{}, &core.OperationError{Category: category, Operation: "resumes.touch", Platform: Name, Message: fmt.Sprintf("HH returned status %d", response.StatusCode)}
}

func (transport *ResumeTouchTransport) authenticatedClient() (*http.Client, error) {
	data, err := os.ReadFile(transport.stateFile)
	if err != nil {
		return nil, &core.OperationError{Category: core.ErrorUnauthorized, Operation: "resumes.touch.auth", Platform: Name, Message: "browser state is unavailable", Cause: err}
	}
	var state browserStorageState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, &core.OperationError{Category: core.ErrorUnauthorized, Operation: "resumes.touch.auth", Platform: Name, Message: "browser state is invalid", Cause: err}
	}
	jar, _ := cookiejar.New(nil)
	for _, rawURL := range []string{transport.profileURL, transport.touchURL} {
		target, _ := url.Parse(rawURL)
		cookies := make([]*http.Cookie, 0, len(state.Cookies))
		for _, item := range state.Cookies {
			if !browserCookieMatchesURL(item, target) {
				continue
			}
			cookie := &http.Cookie{Name: item.Name, Value: item.Value, Domain: item.Domain, Path: item.Path, HttpOnly: item.HTTPOnly, Secure: item.Secure}
			if item.Expires > 0 {
				cookie.Expires = time.Unix(int64(item.Expires), 0)
			}
			cookies = append(cookies, cookie)
		}
		jar.SetCookies(target, cookies)
	}
	copy := *transport.client
	copy.Jar = jar
	return &copy, nil
}

func browserCookieMatchesURL(cookie browserCookie, target *url.URL) bool {
	domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(cookie.Domain)), ".")
	host := strings.ToLower(target.Hostname())
	return domain == "" || host == domain || strings.HasSuffix(host, "."+domain)
}

func (transport *ResumeTouchTransport) readTouchState(ctx context.Context, client *http.Client, resumeID string) (resumeTouchState, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, transport.profileURL, nil)
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
	response, err := client.Do(request)
	if err != nil {
		return resumeTouchState{}, &core.OperationError{Category: core.ErrorTemporaryFailure, Operation: "resumes.touch.state", Platform: Name, Message: "profile request failed", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return resumeTouchState{}, &core.OperationError{Category: core.ErrorUnauthorized, Operation: "resumes.touch.state", Platform: Name, Message: "browser session is unauthorized"}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return resumeTouchState{}, err
	}
	match := initialStatePattern.FindSubmatch(data)
	if len(match) != 2 {
		return resumeTouchState{}, &core.OperationError{
			Category: core.ErrorUnauthorized, Operation: "resumes.touch.state", Platform: Name,
			Message:  "authenticated resume state was not found",
			Metadata: map[string]string{"status": fmt.Sprint(response.StatusCode), "final_path": response.Request.URL.Path},
		}
	}
	var state struct {
		ApplicantResumes []struct {
			Attributes resumeTouchState `json:"_attributes"`
		} `json:"applicantResumes"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(string(match[1]))), &state); err != nil {
		return resumeTouchState{}, fmt.Errorf("decode HH resume state: %w", err)
	}
	for _, resume := range state.ApplicantResumes {
		if resume.Attributes.ID == resumeID || resume.Attributes.Hash == resumeID {
			return resume.Attributes, nil
		}
	}
	return resumeTouchState{}, &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "resumes.touch.state", Platform: Name, Message: "configured resume was not found"}
}

func cookieValue(client *http.Client, target *url.URL, name string) string {
	for _, cookie := range client.Jar.Cookies(target) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func millisTime(value int64) *time.Time {
	if value <= 0 {
		return nil
	}
	result := time.UnixMilli(value).UTC()
	return &result
}

func touchRateLimit(next *time.Time) error {
	return &core.OperationError{Category: core.ErrorRateLimited, Operation: "resumes.touch", Platform: Name, RetryAfter: next, Message: "resume cannot be raised yet"}
}
