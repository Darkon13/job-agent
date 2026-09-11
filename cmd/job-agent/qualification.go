package main

import (
	"bytes"
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

	"github.com/Darkon13/job-agent/core"
)

func runQualification(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	if len(args) == 0 {
		return errors.New("usage: job-agent qualification catalog|sync|start [flags]")
	}
	switch args[0] {
	case "catalog":
		return runQualificationCatalog(ctx, args[1:], output, client)
	case "sync":
		return runQualificationSync(ctx, args[1:], output, client)
	case "start":
		return runQualificationStart(ctx, args[1:], output, client)
	default:
		return fmt.Errorf("unknown qualification command %q", args[0])
	}
}

func runQualificationCatalog(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent qualification catalog", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	profile := flags.String("profile", "", "profile tag")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*profile) == "" {
		return errors.New("usage: job-agent qualification catalog --profile <tag>")
	}
	base, err := authBaseURL(*apiURL)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var body struct {
		Items []core.QualificationOffering `json:"items"`
	}
	endpoint := base + "/api/v1/profiles/" + url.PathEscape(strings.TrimSpace(*profile)) + "/qualifications"
	if err := authJSON(ctx, client, http.MethodGet, endpoint, nil, &body); err != nil {
		return err
	}
	for _, offering := range body.Items {
		best := "-"
		if offering.BestResult != nil {
			best = string(offering.BestResult.Status)
			if offering.BestResult.Score != nil && offering.BestResult.MaxScore != nil {
				best = fmt.Sprintf("%s %.0f/%.0f", best, *offering.BestResult.Score, *offering.BestResult.MaxScore)
			}
		}
		fmt.Fprintf(output, "offering=%s family=%q level=%q status=%s best=%s\n",
			offering.ID, offering.Qualification.FamilyName, offering.Qualification.LevelName, offering.Status, best)
	}
	return nil
}

func runQualificationSync(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent qualification sync", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	profile := flags.String("profile", "", "profile tag")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*profile) == "" {
		return errors.New("usage: job-agent qualification sync --profile <tag>")
	}
	return qualificationPost(ctx, *apiURL, "/api/v1/profiles/"+url.PathEscape(strings.TrimSpace(*profile))+"/qualifications/sync", output, client, nil)
}

func runQualificationStart(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent qualification start", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	profile := flags.String("profile", "", "profile tag")
	offering := flags.String("offering", "", "offering id from the catalog")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*profile) == "" || strings.TrimSpace(*offering) == "" {
		return errors.New("usage: job-agent qualification start --profile <tag> --offering <id>")
	}
	path := "/api/v1/profiles/" + url.PathEscape(strings.TrimSpace(*profile)) +
		"/qualifications/" + url.PathEscape(strings.TrimSpace(*offering)) + "/start"
	return qualificationPost(ctx, *apiURL, path, output, client, nil)
}

func qualificationPost(ctx context.Context, apiURL, path string, output io.Writer, client *http.Client, body any) error {
	base, err := authBaseURL(apiURL)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	key, err := reviewRequestKey()
	if err != nil {
		return err
	}
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
		return fmt.Errorf("decode qualification response: %w", err)
	}
	fmt.Fprintf(output, "task=%s created=%t\n", result.TaskID, result.Created)
	return nil
}
