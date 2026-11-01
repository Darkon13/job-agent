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
	"sync"
	"time"
)

const (
	apiBaseURL   = "https://api.hh.ru"
	oauthBaseURL = "https://hh.ru/oauth"
	userAgent    = "Mozilla/5.0 (Linux; Android 13; Pixel 7 Build/TQ3A.230901.001; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/118.0.0.0 Mobile Safari/537.36"
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

type Client struct {
	http      *http.Client
	tokenFile string
	mu        sync.Mutex
	token     Token
}

func New(tokenFile string) (*Client, error) {
	c := &Client{http: &http.Client{Timeout: 30 * time.Second}, tokenFile: tokenFile}
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

func (c *Client) refresh(ctx context.Context) error {
	form := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": c.token.RefreshToken,
	}
	var token Token
	if err := c.tokenRequest(ctx, form, &token); err != nil {
		return err
	}
	return c.SetToken(token)
}

func (c *Client) Request(ctx context.Context, method, path string, body any, out any) error {
	c.mu.Lock()
	if c.token.AccessToken == "" || (c.token.ExpiresAt > 0 && time.Now().Unix() >= c.token.ExpiresAt) {
		if c.token.RefreshToken == "" {
			c.mu.Unlock()
			return errors.New("hh api: no token, authorize first")
		}
		if err := c.refresh(ctx); err != nil {
			c.mu.Unlock()
			return err
		}
	}
	token := c.token.AccessToken
	c.mu.Unlock()

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
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusForbidden {
		var failure struct {
			Errors []struct {
				Type       string `json:"type"`
				CaptchaURL string `json:"captcha_url"`
			} `json:"errors"`
		}
		if json.Unmarshal(data, &failure) == nil {
			for _, item := range failure.Errors {
				if item.Type == "captcha_required" || item.CaptchaURL != "" {
					return &CaptchaRequiredError{URL: item.CaptchaURL}
				}
			}
		}
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("hh api %s %s: %d: %s", method, path, resp.StatusCode, string(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
