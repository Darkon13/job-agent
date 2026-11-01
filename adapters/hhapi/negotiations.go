package hhapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"encoding/json"
	"io"
)

type ApplyRequest struct {
	VacancyID string `json:"vacancy_id"`
	ResumeID  string `json:"resume_id"`
	Message   string `json:"message,omitempty"`
}

type Negotiation struct {
	ID string `json:"id"`
}

// SetCaptchaSolver задаёт обработчик captcha_url: он должен пройти капчу.
func (c *Client) SetCaptchaSolver(solver func(context.Context, string) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.captchaSolver = solver
}

// Apply sends a response with a cover letter through the mobile API.
// HH expects form-urlencoded fields, not JSON.
func (c *Client) Apply(ctx context.Context, request ApplyRequest) (Negotiation, error) {
	for attempt := 0; attempt < 3; attempt++ {
		result, err := c.applyOnce(ctx, request)
		if err == nil {
			return result, nil
		}
		var captcha *CaptchaRequiredError
		c.mu.Lock()
		solver := c.captchaSolver
		c.mu.Unlock()
		if errors.As(err, &captcha) && solver != nil {
			if solveErr := solver(ctx, captcha.URL); solveErr != nil {
				return Negotiation{}, solveErr
			}
			continue
		}
		return Negotiation{}, err
	}
	return Negotiation{}, fmt.Errorf("hh api: captcha attempts exhausted")
}

func (c *Client) applyOnce(ctx context.Context, request ApplyRequest) (Negotiation, error) {
	c.mu.Lock()
	if c.token.AccessToken == "" || (c.token.ExpiresAt > 0 && time.Now().Unix() >= c.token.ExpiresAt) {
		if c.token.RefreshToken == "" {
			c.mu.Unlock()
			return Negotiation{}, fmt.Errorf("hh api: no token, authorize first")
		}
		if err := c.refresh(ctx); err != nil {
			c.mu.Unlock()
			return Negotiation{}, err
		}
	}
	token := c.token.AccessToken
	c.mu.Unlock()

	form := url.Values{
		"vacancy_id": {request.VacancyID},
		"resume_id":  {request.ResumeID},
	}
	if strings.TrimSpace(request.Message) != "" {
		form.Set("message", request.Message)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+"/negotiations", strings.NewReader(form.Encode()))
	if err != nil {
		return Negotiation{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Force-App-Access", "true")
	req.Header.Set("X-HH-App-Active", "true")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return Negotiation{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusForbidden {
		if captcha := parseCaptcha(body); captcha != nil {
			return Negotiation{}, captcha
		}
	}
	if resp.StatusCode >= 300 {
		return Negotiation{}, fmt.Errorf("hh api applications: %d: %s", resp.StatusCode, string(body))
	}
	var result Negotiation
	_ = json.Unmarshal(body, &result)
	return result, nil
}

func parseCaptcha(body []byte) error {
	var failure struct {
		Errors []struct {
			Type       string `json:"type"`
			CaptchaURL string `json:"captcha_url"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &failure) != nil {
		return nil
	}
	for _, item := range failure.Errors {
		if item.Type == "captcha_required" || item.CaptchaURL != "" {
			return &CaptchaRequiredError{URL: item.CaptchaURL}
		}
	}
	return nil
}

// NegotiationID ищет id отклика по вакансии среди последних переговоров.
func (c *Client) NegotiationID(ctx context.Context, vacancyID string) string {
	var page struct {
		Items []struct {
			ID      string `json:"id"`
			Vacancy struct {
				ID string `json:"id"`
			} `json:"vacancy"`
		} `json:"items"`
	}
	if err := c.Request(ctx, "GET", "/negotiations?per_page=50", nil, &page); err != nil {
		return ""
	}
	for _, item := range page.Items {
		if item.Vacancy.ID == vacancyID {
			return item.ID
		}
	}
	return ""
}
