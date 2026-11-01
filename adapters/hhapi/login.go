package hhapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const authorizeParams = "force_login=true&role=applicant&force_role=true&skip_choose_account=true"

type LoginSession struct {
	client   *http.Client
	loginURL string
	xsrf     string
	pubKey   *rsa.PublicKey
}

func (c *Client) newLoginJar() (http.CookieJar, error) {
	return cookiejar.New(nil)
}

// BeginLogin открывает страницу OAuth-логина и извлекает xsrf/публичный ключ.
func (c *Client) BeginLogin(ctx context.Context, clientID string) (*LoginSession, error) {
	jar, err := c.newLoginJar()
	if err != nil {
		return nil, err
	}
	follow := &http.Client{Timeout: 60 * time.Second, Jar: jar}
	loginURL := AuthorizeURL(clientID, "", "") + "&" + authorizeParams
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := follow.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hh login page: %d", resp.StatusCode)
	}
	page := html.UnescapeString(string(body))
	start := strings.Index(page, `id="HH-Lux-InitialState">`)
	end := strings.Index(page[start:], "</template>")
	if start < 0 || end < 0 {
		return nil, errors.New("hh login page: initial state not found")
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(page[start+len(`id="HH-Lux-InitialState">`):start+end]), &state); err != nil {
		return nil, fmt.Errorf("parse initial state: %w", err)
	}
	xsrf, _ := findString(state, "xsrfToken")
	secret, _ := findString(state, "loginTrustFlagsSecret")
	if xsrf == "" || secret == "" {
		return nil, errors.New("hh login page: xsrf or trust key missing")
	}
	block, _ := pem.Decode([]byte(secret))
	if block == nil {
		return nil, errors.New("hh login page: invalid trust key pem")
	}
	keyAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse trust key: %w", err)
	}
	key, ok := keyAny.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("hh login page: trust key is not RSA")
	}
	noRedirect := &http.Client{Timeout: 60 * time.Second, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &LoginSession{client: noRedirect, loginURL: resp.Request.URL.String(), xsrf: xsrf, pubKey: key}, nil
}

func findString(value any, key string) (string, bool) {
	switch v := value.(type) {
	case map[string]any:
		if item, ok := v[key].(string); ok && item != "" {
			return item, true
		}
		for _, item := range v {
			if found, ok := findString(item, key); ok {
				return found, true
			}
		}
	case []any:
		for _, item := range v {
			if found, ok := findString(item, key); ok {
				return found, true
			}
		}
	}
	return "", false
}

func (s *LoginSession) trustFlags(payload map[string]any) (string, error) {
	payload["ts"] = time.Now().UnixMilli()
	plain, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, s.pubKey, plain, nil)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func (s *LoginSession) postForm(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Hhtmfrom", "")
	req.Header.Set("X-Hhtmsource", "account_login")
	req.Header.Set("Referer", s.loginURL)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func (s *LoginSession) LoginPassword(ctx context.Context, username, password string) error {
	flags, err := s.trustFlags(map[string]any{"password": map[string]any{"suggest": true, "paste": false}})
	if err != nil {
		return err
	}
	sp, _ := url.Parse(s.loginURL)
	failURL := sp.Path + "?" + sp.RawQuery
	body, err := s.postForm(ctx, s.loginURL, url.Values{
		"accountType":     {"APPLICANT"},
		"failUrl":         {failURL},
		"remember":        {"true"},
		"loginTrustFlags": {flags},
		"username":        {username},
		"password":        {password},
		"captchaText":     {""},
	})
	if err != nil {
		return err
	}
	var result struct {
		LoginError map[string]any `json:"loginError"`
	}
	_ = json.Unmarshal(body, &result)
	if result.LoginError != nil {
		return fmt.Errorf("hh login failed: %v", result.LoginError["code"])
	}
	return nil
}

func (s *LoginSession) RequestOTP(ctx context.Context, username string, isEmail bool) error {
	flags, err := s.trustFlags(map[string]any{"emailOrPhone": map[string]any{"paste": false, "suggest": true}})
	if err != nil {
		return err
	}
	otpType := "phone"
	if isEmail {
		otpType = "email"
	}
	base, _ := url.Parse(s.loginURL)
	body, err := s.postForm(ctx, base.Scheme+"://"+base.Host+"/account/otp_generate", url.Values{
		"login":           {username},
		"otpType":         {otpType},
		"operationType":   {"applicant_otp_auth"},
		"authScenario":    {"APPLICANT_AUTH"},
		"formatPhone":     {"true"},
		"isSignupPage":    {"false"},
		"loginTrustFlags": {flags},
		"captchaText":     {""},
	})
	if err != nil {
		return err
	}
	var result struct {
		Success bool `json:"success"`
	}
	_ = json.Unmarshal(body, &result)
	if !result.Success {
		return fmt.Errorf("hh otp request rejected: %s", string(body))
	}
	return nil
}

func (s *LoginSession) ConfirmOTP(ctx context.Context, username, code string) error {
	flags, err := s.trustFlags(nil)
	if err != nil {
		return err
	}
	base, _ := url.Parse(s.loginURL)
	body, err := s.postForm(ctx, base.Scheme+"://"+base.Host+"/account/login/by_code", url.Values{
		"username":          {username},
		"code":              {code},
		"backurl":           {s.loginURL},
		"operationType":     {"applicant_otp_auth"},
		"isApplicantSignup": {"false"},
		"remember":          {"true"},
		"loginTrustFlags":   {flags},
		"authScenario":      {"APPLICANT_AUTH"},
	})
	if err != nil {
		return err
	}
	var result struct {
		Success bool `json:"success"`
	}
	_ = json.Unmarshal(body, &result)
	if !result.Success {
		return fmt.Errorf("hh otp rejected: %s", string(body))
	}
	return nil
}

// FinishOAuth забирает 302 на hhandroid://oauthresponse и меняет code на токен.
func (s *LoginSession) FinishOAuth(ctx context.Context, c *Client, clientID, clientSecret string) error {
	sp, _ := url.Parse(s.loginURL)
	backurl := sp.Query().Get("backurl")
	if backurl == "" {
		return errors.New("hh login: backurl missing")
	}
	target := backurl
	if u, err := url.Parse(backurl); err != nil || !u.IsAbs() {
		base := &url.URL{Scheme: sp.Scheme, Host: sp.Host}
		target = base.ResolveReference(&url.URL{Path: backurl, RawQuery: u.RawQuery}).String()
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", s.loginURL)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc, "hhandroid://oauthresponse") {
		return fmt.Errorf("hh login: unexpected redirect %d %q", resp.StatusCode, loc)
	}
	locURL, err := url.Parse(loc)
	if err != nil {
		return err
	}
	code := locURL.Query().Get("code")
	if code == "" {
		return errors.New("hh login: no code in redirect")
	}
	return c.ExchangeCode(ctx, clientID, clientSecret, code)
}
