package hhapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"
)

const captchaSuccessBackURL = "hh://applicant/captchaSuccessBackUrl"

// SolveCaptcha проходит отдельную страницу капчи и отправляет ответ.
// answer получает PNG-картинку и возвращает текст с неё.
func SolveCaptcha(ctx context.Context, captchaURL string, answer func(image []byte) (string, error)) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 60 * time.Second, Jar: jar}
	follow := *client
	follow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	u, err := url.Parse(captchaURL)
	if err != nil {
		return err
	}
	query := u.Query()
	query.Set("backurl", captchaSuccessBackURL)
	u.RawQuery = query.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	finalURL := resp.Request.URL
	state := finalURL.Query().Get("state")
	if state == "" {
		return fmt.Errorf("hh captcha: state missing in %s", finalURL)
	}
	xsrf := ""
	for _, cookie := range jar.Cookies(finalURL) {
		if cookie.Name == "_xsrf" {
			xsrf = cookie.Value
		}
	}
	if xsrf == "" {
		return fmt.Errorf("hh captcha: _xsrf cookie missing")
	}
	key, err := captchaKey(ctx, client, finalURL, xsrf)
	if err != nil {
		return err
	}
	imageURL := "https://hh.ru/captcha/picture?key=" + url.QueryEscape(key)
	imageReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	imageReq.Header.Set("User-Agent", userAgent)
	imageReq.Header.Set("Accept", "image/avif,image/webp,image/png,image/svg+xml,image/*;q=0.8,*/*;q=0.5")
	imageReq.Header.Set("Referer", finalURL.String())
	imageResp, err := client.Do(imageReq)
	if err != nil {
		return err
	}
	image, err := io.ReadAll(io.LimitReader(imageResp.Body, 4<<20))
	imageResp.Body.Close()
	if err != nil || len(image) == 0 {
		return fmt.Errorf("hh captcha: empty image")
	}
	text, err := answer(image)
	if err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("hh captcha: empty answer")
	}

	time.Sleep(12 * time.Second)
	payload := url.Values{
		"captchaText":  {text},
		"captchaKey":   {key},
		"captchaState": {state},
		"backurl":      {captchaSuccessBackURL},
		"failurl":      {"/account/captcha?state=" + url.QueryEscape(state) + "&backurl=" + url.QueryEscape(captchaSuccessBackURL)},
	}
	target := "https://hh.ru/account/captcha?" + payload.Encode()
	submit, _ := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	setXHR(submit, finalURL, xsrf)
	resp2, err := follow.Do(submit)
	if err != nil {
		return err
	}
	body2, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
	_ = os.WriteFile("/tmp/hh-captcha-submit.html", body2, 0o600)
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusFound || resp2.StatusCode == http.StatusOK || resp2.StatusCode == http.StatusNoContent {
		return nil
	}
	return fmt.Errorf("hh captcha: submit status %d: %s", resp2.StatusCode, string(body2))
}

func captchaKey(ctx context.Context, client *http.Client, reference *url.URL, xsrf string) (string, error) {
	target := "https://hh.ru/captcha?lang=EN"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	setXHR(req, reference, xsrf)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("hh captcha key: %s", string(body))
	}
	if result.Key == "" {
		return "", fmt.Errorf("hh captcha key missing")
	}
	return result.Key, nil
}

func setXHR(req *http.Request, reference *url.URL, xsrf string) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-XSRFToken", xsrf)
	req.Header.Set("Origin", "https://hh.ru")
	req.Header.Set("Referer", reference.String())
	req.Header.Set("X-Hhtmfrom", "")
	req.Header.Set("X-Hhtmsource", "account_captcha")
}
