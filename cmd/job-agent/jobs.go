package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// jobListItemView is the operator-facing job view returned by the dashboard
// API: the runnable action, its schedules and the pause state.
type jobListItemView struct {
	Tag         string   `json:"tag"`
	Description string   `json:"description,omitempty"`
	TaskType    string   `json:"task_type"`
	Profiles    []string `json:"profiles,omitempty"`
	ProfileID   string   `json:"profile_id,omitempty"`
	Paused      bool     `json:"paused"`
	Pauses      []struct {
		ProfileID string `json:"profile_id"`
		Reason    string `json:"reason,omitempty"`
	} `json:"pauses,omitempty"`
	Schedules []struct {
		Interval   string    `json:"interval,omitempty"`
		Expression string    `json:"expression,omitempty"`
		Timezone   string    `json:"timezone,omitempty"`
		NextRunAt  time.Time `json:"next_run_at"`
	} `json:"schedules,omitempty"`
}

func runJobs(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	if len(args) == 0 {
		return errors.New("usage: job-agent jobs list|pause|resume [flags]")
	}
	switch args[0] {
	case "list":
		return runJobsList(ctx, args[1:], output, client)
	case "pause":
		return runJobsSetPaused(ctx, args[1:], output, client, true)
	case "resume":
		return runJobsSetPaused(ctx, args[1:], output, client, false)
	default:
		return fmt.Errorf("unknown jobs command %q", args[0])
	}
}

func runJobsList(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent jobs list", flag.ContinueOnError)
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
		client = &http.Client{Timeout: 30 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/jobs", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("jobs list failed: %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var listing struct {
		Items []jobListItemView `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listing); err != nil {
		return fmt.Errorf("decode jobs list: %w", err)
	}
	for _, item := range listing.Items {
		state := "работает"
		if item.Paused {
			state = "на паузе"
		}
		name := item.Description
		if name == "" {
			name = item.Tag
		}
		fmt.Fprintf(output, "%-40s %-24s %-12s %s\n", item.Tag, item.TaskType, state, name)
		for _, schedule := range item.Schedules {
			cadence := schedule.Expression
			if schedule.Interval != "" {
				cadence = "каждые " + schedule.Interval
			}
			fmt.Fprintf(output, "    %s: %s\n", cadence, schedule.NextRunAt.Format(time.RFC3339))
		}
	}
	return nil
}

func runJobsSetPaused(ctx context.Context, args []string, output io.Writer, client *http.Client, paused bool) error {
	action := "resume"
	if paused {
		action = "pause"
	}
	flags := flag.NewFlagSet("job-agent jobs "+action, flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	profileID := flags.String("profile", "", "profile id (default: every profile of the job)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	tag := strings.TrimSpace(flags.Arg(0))
	if tag == "" {
		return fmt.Errorf("usage: job-agent jobs %s <tag> [--profile id]", action)
	}
	base, err := authBaseURL(*apiURL)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/api/v1/jobs/%s/%s", base, url.PathEscape(tag), action)
	if profile := strings.TrimSpace(*profileID); profile != "" {
		endpoint += "?profile_id=" + url.QueryEscape(profile)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("job %s failed: %s: %s", action, response.Status, strings.TrimSpace(string(message)))
	}
	var result struct {
		Tag       string `json:"tag"`
		ProfileID string `json:"profile_id"`
		Paused    bool   `json:"paused"`
		Affected  int    `json:"affected"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode job %s result: %w", action, err)
	}
	state := "снова выполняется"
	if result.Paused {
		state = "на паузе"
	}
	fmt.Fprintf(output, "job %s: %s (затронуто профилей: %d)\n", result.Tag, state, result.Affected)
	return nil
}
