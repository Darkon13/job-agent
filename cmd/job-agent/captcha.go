package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/presenter"
)

const maximumCaptchaImage = 4 << 20

// captchaSession mirrors the browser check payload of the backend API.
type captchaSession struct {
	SessionID     string `json:"session_id"`
	ApplicationID string `json:"application_id"`
	ProfileID     string `json:"profile_id"`
	State         string `json:"state"`
	Message       string `json:"message"`
	HasImage      bool   `json:"has_image"`
}

func runCaptcha(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	if len(args) == 0 {
		return errors.New("usage: job-agent captcha solve <application_id> | list [--api URL]")
	}
	switch args[0] {
	case "solve":
		return runCaptchaSolve(ctx, args[1:], output, client)
	case "list":
		return runCaptchaList(ctx, args[1:], output, client)
	default:
		return fmt.Errorf("unknown captcha command %q", args[0])
	}
}

// runCaptchaSolve completes one blocked application through the profile's
// browser: the operator answers the captcha in the terminal while the page
// stays open on the backend.
func runCaptchaSolve(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent captcha solve", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	imageProtocol := flags.String("image-protocol", "auto", "auto, kitty, sixel, unicode or file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	applicationID := strings.TrimSpace(flags.Arg(0))
	if applicationID == "" {
		return errors.New("usage: job-agent captcha solve <application_id> [--api URL] [--image-protocol auto]")
	}
	protocol, err := presenter.ParseProtocol(*imageProtocol)
	if err != nil {
		return err
	}
	base, err := authBaseURL(*apiURL)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if err := checkAuthAPIVersion(ctx, client, base); err != nil {
		return err
	}
	endpoint := base + "/api/v1/applications/" + url.PathEscape(applicationID) + "/browser-check"
	var session captchaSession
	if err := authJSON(ctx, client, http.MethodPost, endpoint, struct{}{}, &session); err != nil {
		return fmt.Errorf("start browser check: %w", err)
	}
	for {
		switch session.State {
		case "done":
			fmt.Fprintf(output, "DONE %s\n", session.Message)
			return nil
		case "review":
			if session.HasImage {
				if err := renderCaptchaImage(ctx, client, base, session, protocol); err != nil {
					return err
				}
			}
			fmt.Fprintf(output, "REVIEW %s\n", session.Message)
			return nil
		case "waiting_captcha":
			if err := renderCaptchaImage(ctx, client, base, session, protocol); err != nil {
				return err
			}
			value, err := promptTTY("Символы с картинки: ")
			if err != nil {
				return err
			}
			if strings.TrimSpace(value) == "" {
				return errors.New("пустой ответ на капчу")
			}
			var next captchaSession
			answerEndpoint := endpoint + "/" + url.PathEscape(session.SessionID) + "/answer"
			if err := authJSON(ctx, client, http.MethodPost, answerEndpoint, map[string]string{"value": value}, &next); err != nil {
				return fmt.Errorf("answer captcha: %w", err)
			}
			session = next
		default:
			return fmt.Errorf("unexpected browser check state %q", session.State)
		}
	}
}

// runCaptchaList prints applications that wait for a captcha decision.
func runCaptchaList(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent captcha list", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	base, err := authBaseURL(*apiURL)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if err := checkAuthAPIVersion(ctx, client, base); err != nil {
		return err
	}
	var listing struct {
		Items []struct {
			ID           string `json:"id"`
			ProfileID    string `json:"profile_id"`
			DecisionCode string `json:"decision_code"`
			VacancyTitle string `json:"vacancy_title"`
		} `json:"items"`
	}
	endpoint := base + "/api/v1/applications?status=waiting_validation&limit=200"
	if err := authJSON(ctx, client, http.MethodGet, endpoint, nil, &listing); err != nil {
		return fmt.Errorf("list applications: %w", err)
	}
	count := 0
	for _, item := range listing.Items {
		if item.DecisionCode != "captcha_required" {
			continue
		}
		count++
		fmt.Fprintf(output, "%s\t%s\t%s\n", item.ID, item.ProfileID, item.VacancyTitle)
	}
	if count == 0 {
		fmt.Fprintln(output, "нет откликов, ожидающих капчу")
	}
	return nil
}

// renderCaptchaImage downloads one PNG and shows it through the presenter.
func renderCaptchaImage(ctx context.Context, client *http.Client, base string, session captchaSession, protocol presenter.Protocol) error {
	endpoint := base + "/api/v1/applications/" + url.PathEscape(session.ApplicationID) +
		"/browser-check/" + url.PathEscape(session.SessionID) + "/image"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumCaptchaImage+1))
	if err != nil {
		return err
	}
	if len(body) > maximumCaptchaImage {
		return errors.New("captcha image is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return authResponseError(response.StatusCode, body)
	}
	return renderChallenge(body, protocol)
}
