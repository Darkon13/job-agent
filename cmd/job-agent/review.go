package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
)

const maximumReviewJSONResponse = 1 << 20

func runReview(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	if len(args) == 0 {
		return errors.New("usage: job-agent review show|answer [flags]")
	}
	switch args[0] {
	case "show":
		return runReviewShow(ctx, args[1:], output, client)
	case "answer":
		return runReviewAnswer(ctx, args[1:], output, client)
	default:
		return fmt.Errorf("unknown review command %q", args[0])
	}
}

type reviewSessionView struct {
	ID         core.ReviewSessionID     `json:"id"`
	ProfileID  core.ProfileID           `json:"profile_id"`
	Status     core.ReviewSessionStatus `json:"status"`
	Revision   uint64                   `json:"revision"`
	Prompt     *reviewPromptView        `json:"prompt,omitempty"`
	Selections []core.ReviewSelection   `json:"selections,omitempty"`
}

type reviewPromptView struct {
	ID       core.ReviewPromptID `json:"id"`
	Revision uint64              `json:"revision"`
	Question core.Question       `json:"question"`
}

func runReviewShow(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent review show", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	sessionID := flags.String("session", "", "review session id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*sessionID) == "" {
		return errors.New("usage: job-agent review show --session <id>")
	}
	base, err := authBaseURL(*apiURL)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var session reviewSessionView
	endpoint := base + "/api/v1/review-sessions/" + url.PathEscape(strings.TrimSpace(*sessionID))
	if err := authJSON(ctx, client, http.MethodGet, endpoint, nil, &session); err != nil {
		return err
	}
	fmt.Fprintf(output, "session=%s profile=%s status=%s revision=%d\n", session.ID, session.ProfileID, session.Status, session.Revision)
	if session.Prompt == nil {
		fmt.Fprintln(output, "prompt=<none>")
		return nil
	}
	fmt.Fprintf(output, "prompt=%s question=%q\n", session.Prompt.ID, session.Prompt.Question.Text)
	for _, option := range session.Prompt.Question.Options {
		fmt.Fprintf(output, "option=%q\n", option.Text)
	}
	return nil
}

func runReviewAnswer(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent review answer", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	sessionID := flags.String("session", "", "review session id")
	promptID := flags.String("prompt", "", "review prompt id")
	revision := flags.Uint64("revision", 0, "expected session revision")
	text := flags.String("text", "", "free-text answer")
	source := flags.String("source", "cli", "answer source")
	var options stringList
	flags.Var(&options, "option", "selected option text (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*sessionID) == "" || strings.TrimSpace(*promptID) == "" || *revision == 0 {
		return errors.New("usage: job-agent review answer --session <id> --prompt <id> --revision <n> --option <text>|--text <text>")
	}
	if *text == "" && len(options) == 0 {
		return errors.New("review answer requires --option or --text")
	}
	if *text != "" && len(options) != 0 {
		return errors.New("review answer accepts either --option or --text, not both")
	}
	base, err := authBaseURL(*apiURL)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	body := map[string]any{
		"prompt_id": promptID, "expected_revision": *revision,
		"selected_options": []string(options), "text": *text, "source": *source,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	key, err := reviewRequestKey()
	if err != nil {
		return err
	}
	endpoint := base + "/api/v1/review-sessions/" + url.PathEscape(strings.TrimSpace(*sessionID)) + "/answers"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumReviewJSONResponse+1))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return authResponseError(response.StatusCode, data)
	}
	var result struct {
		TaskID  string `json:"task_id"`
		Created bool   `json:"created"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode review answer response: %w", err)
	}
	fmt.Fprintf(output, "task=%s created=%t\n", result.TaskID, result.Created)
	return nil
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }

func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func reviewRequestKey() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate review request key: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
