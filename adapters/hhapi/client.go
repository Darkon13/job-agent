package hhapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	apiBaseURL   = "https://api.hh.ru"
	oauthBaseURL = "https://hh.ru/oauth"
	userAgent    = "Mozilla/5.0 (Linux; Android 13; Pixel 7 Build/TQ3A.230901.001; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/118.0.0.0 Mobile Safari/537.36"

	// defaultRequestDelay matches hh-applicant-tool's DEFAULT_DELAY. The
	// mobile client keeps a small gap between requests instead of relying on a
	// large per-application sleep.
	defaultRequestDelay = 345 * time.Millisecond
)

type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"access_expires_at"`
}

type CaptchaRequiredError struct {
	URL string
}

func (e *CaptchaRequiredError) Error() string { return "hh captcha required: " + e.URL }

// APIError is returned for a non-successful mobile API response. The adapter
// uses the status and body to preserve HH's distinction between quota, test,
// validation and authorization failures.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
	RetryAfter time.Time
}

func (e *APIError) Error() string {
	return fmt.Sprintf("hh api %s %s: %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

type Client struct {
	http          *http.Client
	tokenFile     string
	cookieFile    string
	mu            sync.Mutex
	token         Token
	userAgent     string
	cookies       *persistentCookieJar
	captchaSolver func(context.Context, string) error

	requestMu       sync.Mutex
	delay           time.Duration
	lastRequestDone time.Time

	cookieMu       sync.Mutex
	cookieModTime  time.Time
	lastCookieSave time.Time
}

func New(tokenFile string) (*Client, error) {
	cookieFile := cookieFilePath()
	jar := loadCookieJar(cookieFile)
	c := &Client{
		http: &http.Client{Timeout: 30 * time.Second, Jar: jar}, tokenFile: tokenFile,
		cookieFile: cookieFile, userAgent: rotateDeviceUserAgent(), cookies: jar,
		delay: defaultRequestDelay,
	}
	if info, err := os.Stat(cookieFile); err == nil {
		c.cookieModTime = info.ModTime()
	}
	data, err := os.ReadFile(tokenFile)
	if err == nil {
		if err := json.Unmarshal(data, &c.token); err != nil {
			return nil, fmt.Errorf("decode token file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return c, nil
}

func (c *Client) SetToken(token Token) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}
	return os.WriteFile(c.tokenFile, data, 0o600)
}

func (c *Client) Token() Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// SetDelay changes the minimum gap between mobile API requests. The reference
// client uses 345ms; a non-positive value disables the gap.
func (c *Client) SetDelay(delay time.Duration) {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	c.delay = delay
}

func (c *Client) refresh(ctx context.Context) error {
	lock, err := os.OpenFile(c.tokenFile+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }()
		if data, readErr := os.ReadFile(c.tokenFile); readErr == nil {
			var disk Token
			c.mu.Lock()
			currentAccessToken := c.token.AccessToken
			c.mu.Unlock()
			if json.Unmarshal(data, &disk) == nil && disk.AccessToken != "" && disk.AccessToken != currentAccessToken {
				c.mu.Lock()
				c.token = disk
				c.mu.Unlock()
				if disk.ExpiresAt == 0 || time.Now().Unix() < disk.ExpiresAt {
					return nil
				}
			}
		}
	}
	c.mu.Lock()
	current := c.token
	c.mu.Unlock()
	if strings.TrimSpace(current.RefreshToken) == "" {
		return errors.New("hh api: refresh token is missing")
	}
	form := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": current.RefreshToken,
	}
	var token Token
	if err := c.tokenRequest(ctx, form, &token); err != nil {
		return err
	}
	return c.SetToken(token)
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	if token.AccessToken == "" || (token.ExpiresAt > 0 && time.Now().Unix() >= token.ExpiresAt) {
		if token.RefreshToken == "" {
			return "", errors.New("hh api: no token, authorize first")
		}
		if err := c.refresh(ctx); err != nil {
			return "", err
		}
		c.mu.Lock()
		token = c.token
		c.mu.Unlock()
	}
	return token.AccessToken, nil
}

func (c *Client) Request(ctx context.Context, method, path string, body any, out any) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := c.requestOnce(ctx, method, path, body, out)
		if err == nil {
			return nil
		}
		var captcha *CaptchaRequiredError
		c.mu.Lock()
		solver := c.captchaSolver
		c.mu.Unlock()
		if errors.As(err, &captcha) && solver != nil {
			if solveErr := solver(ctx, captcha.URL); solveErr != nil {
				return fmt.Errorf("%w: %v", captcha, solveErr)
			}
			continue
		}
		return err
	}
	return fmt.Errorf("hh api: captcha attempts exhausted")
}

func (c *Client) requestOnce(ctx context.Context, method, path string, body any, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("X-HH-App-Active", "true")

	c.reloadCookies()
	status, header, data, err := c.do(ctx, req)
	c.saveCookies(false)
	if err != nil {
		return err
	}
	if status == http.StatusForbidden {
		if captcha := parseCaptcha(data); captcha != nil {
			return captcha
		}
	}
	if status >= 300 {
		return &APIError{Method: method, Path: path, StatusCode: status, Body: string(data), RetryAfter: retryAfterHeader(header)}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// do serializes requests and maintains the reference client's inter-request
// delay. The lock intentionally covers the response read so a concurrent
// caller cannot start while the previous response is still being consumed.
func (c *Client) do(ctx context.Context, req *http.Request) (int, http.Header, []byte, error) {
	return c.doWithClient(ctx, c.http, req)
}

func (c *Client) doWithClient(ctx context.Context, client *http.Client, req *http.Request) (int, http.Header, []byte, error) {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	if c.delay > 0 && !c.lastRequestDone.IsZero() {
		wait := time.Until(c.lastRequestDone.Add(c.delay))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return 0, nil, nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	response, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	c.lastRequestDone = time.Now()
	return response.StatusCode, response.Header, body, nil
}

func retryAfterHeader(header http.Header) time.Time {
	if header == nil {
		return time.Time{}
	}
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return time.Time{}
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return time.Time{}
		}
		return time.Now().Add(time.Duration(seconds) * time.Second)
	}
	if when, err := http.ParseTime(value); err == nil {
		return when
	}
	return time.Time{}
}
