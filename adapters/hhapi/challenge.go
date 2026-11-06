package hhapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	mrand "math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type captchaChallenge struct {
	client     *http.Client
	cookieFile string
	userAgent  string
	page       *url.URL
	xsrf       string
	key        string
	state      string
	startedAt  time.Time
}

var (
	challengeMu sync.Mutex
	challenges  = map[string]*captchaChallenge{}
)

func StartCaptcha(ctx context.Context, captchaURL string) (string, []byte, error) {
	cookieFile := cookieFilePath()
	userAgent := deviceUserAgent()
	jar := loadCookieJar(cookieFile)
	follow := &http.Client{Timeout: 60 * time.Second, Jar: jar}
	u, err := url.Parse(captchaURL)
	if err != nil {
		return "", nil, err
	}
	q := u.Query()
	q.Set("backurl", captchaSuccessBackURL)
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
	resp, err := follow.Do(req)
	if err != nil {
		return "", nil, err
	}
	resp.Body.Close()
	final := resp.Request.URL
	state := final.Query().Get("state")
	xsrf := ""
	for _, cookie := range jar.Cookies(final) {
		if cookie.Name == "_xsrf" {
			xsrf = cookie.Value
		}
	}
	if state == "" || xsrf == "" {
		return "", nil, fmt.Errorf("hh captcha: state or xsrf missing")
	}
	noRedirect := &http.Client{Timeout: 60 * time.Second, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	key, err := captchaKey(ctx, noRedirect, userAgent, final, xsrf)
	if err != nil {
		return "", nil, err
	}
	base := final.Scheme + "://" + final.Host
	imageURL := base + "/captcha/picture?key=" + url.QueryEscape(key)
	imageReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	imageReq.Header.Set("User-Agent", deviceUserAgent())
	imageReq.Header.Set("Accept", "image/avif,image/webp,image/png,image/svg+xml,image/*;q=0.8,*/*;q=0.5")
	imageReq.Header.Set("Referer", final.String())
	imageResp, err := follow.Do(imageReq)
	if err != nil {
		return "", nil, err
	}
	image, _ := io.ReadAll(io.LimitReader(imageResp.Body, 4<<20))
	imageResp.Body.Close()
	if len(image) == 0 {
		return "", nil, fmt.Errorf("hh captcha: empty image")
	}
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	id := hex.EncodeToString(buf)
	_ = saveCookieJar(cookieFile, jar)
	challengeMu.Lock()
	challenges[id] = &captchaChallenge{
		client: noRedirect, cookieFile: cookieFile, userAgent: userAgent,
		page: final, xsrf: xsrf, key: key, state: state, startedAt: time.Now(),
	}
	challengeMu.Unlock()
	return id, image, nil
}

func AnswerCaptcha(ctx context.Context, id, text string) error {
	challengeMu.Lock()
	challenge := challenges[id]
	challengeMu.Unlock()
	if challenge == nil {
		return fmt.Errorf("hh captcha: unknown challenge")
	}
	delay := time.Duration(10000+mrand.Intn(5001)) * time.Millisecond
	if wait := delay - time.Since(challenge.startedAt); wait > 0 {
		time.Sleep(wait)
	}
	quotedBack := strings.ReplaceAll(url.QueryEscape(captchaSuccessBackURL), "%2F", "/")
	failURL := "/account/captcha?state=" + url.QueryEscape(challenge.state) + "&backurl=" + quotedBack
	payload := url.Values{
		"captchaText": {strings.TrimSpace(text)}, "captchaKey": {challenge.key},
		"captchaState": {challenge.state}, "backurl": {captchaSuccessBackURL}, "failurl": {failURL},
	}
	base := challenge.page.Scheme + "://" + challenge.page.Host
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/account/captcha?"+payload.Encode(), nil)
	setXHR(req, challenge.userAgent, challenge.page, challenge.xsrf)
	resp, err := challenge.client.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("hh captcha: submit status %d: %s", resp.StatusCode, string(body))
	}
	_ = saveCookieJar(challenge.cookieFile, challenge.client.Jar)
	challengeMu.Lock()
	delete(challenges, id)
	challengeMu.Unlock()
	return nil
}
