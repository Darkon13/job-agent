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
	"os"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
)

const maximumProfileBootstrapResponse = 1 << 20

func runProfileStartup(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	withApply := make([]string, 0, len(args)+1)
	withApply = append(withApply, "--apply")
	withApply = append(withApply, args...)
	return runProfileBootstrap(ctx, withApply, output, client)
}

func runProfileBootstrap(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent profile bootstrap", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	apply := flags.Bool("apply", false, "enqueue the immutable plan after it is created")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: job-agent profile bootstrap [--api URL] [--apply] <resume.json>")
	}
	data, err := os.ReadFile(flags.Arg(0))
	if err != nil {
		return fmt.Errorf("read profile bootstrap manifest: %w", err)
	}
	manifest, err := core.DecodeProfileBootstrapManifest(data)
	if err != nil {
		return err
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode profile bootstrap manifest: %w", err)
	}
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(*apiURL), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return errors.New("profile bootstrap --api must be an absolute http or https URL")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	endpoint := base.String() + "/api/v1/profile-state/bootstrap/plans"
	var proposal core.ProfileStateProposal
	if err := profileBootstrapRequest(ctx, client, http.MethodPost, endpoint, canonical, &proposal); err != nil {
		return fmt.Errorf("plan profile bootstrap: %w", err)
	}
	fmt.Fprintf(output, "PLAN proposal=%s status=%s changes=%d manifest=%s\n", proposal.ID, proposal.Status, len(proposal.Changes), proposal.ManifestDigest)
	for _, change := range proposal.Changes {
		fmt.Fprintf(output, "  %s %s\n", strings.ToUpper(change.Operation), change.Path)
	}
	if !*apply || proposal.Status == core.ProfileStateProposalNoChanges {
		return nil
	}
	var task core.Task
	if err := profileBootstrapRequest(ctx, client, http.MethodPost, base.String()+"/api/v1/profile-state/proposals/"+url.PathEscape(string(proposal.ID))+"/apply", nil, &task); err != nil {
		return fmt.Errorf("apply profile bootstrap: %w", err)
	}
	fmt.Fprintf(output, "APPLY task=%s status=%s\n", task.ID, task.Status)
	return nil
}

func profileBootstrapRequest(ctx context.Context, client *http.Client, method, endpoint string, body []byte, target any) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumProfileBootstrapResponse))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &problem) == nil && strings.TrimSpace(problem.Error) != "" {
			return fmt.Errorf("backend returned %d: %s", response.StatusCode, problem.Error)
		}
		return fmt.Errorf("backend returned %d", response.StatusCode)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode backend response: %w", err)
	}
	return nil
}
