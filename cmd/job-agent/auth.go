package main

import (
	"bufio"
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
	"path/filepath"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapters/hh"
	"github.com/Darkon13/job-agent/auth"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/credentials"
	"github.com/Darkon13/job-agent/presenter"
)

const (
	maximumAuthJSONResponse = 1 << 20
	maximumAuthChallenge    = 4 << 20
)

func runAuth(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	if len(args) == 0 {
		return errors.New("usage: job-agent auth login|status [flags]")
	}
	switch args[0] {
	case "login":
		return runAuthLogin(ctx, args[1:], output, client)
	case "status":
		return runAuthStatus(ctx, args[1:], output, client)
	case "import":
		return runAuthImport(args[1:], output)
	default:
		return fmt.Errorf("unknown auth command %q", args[0])
	}
}

// runAuthImport narrows an exported Playwright storage state to HH domains and
// writes it to the profile state file without contacting the backend.
func runAuthImport(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("job-agent auth import", flag.ContinueOnError)
	flags.SetOutput(output)
	source := flags.String("source", "", "Playwright storage state export")
	stateOutput := flags.String("state-output", "", "destination state file")
	force := flags.Bool("force", false, "replace an existing state file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*source) == "" || strings.TrimSpace(*stateOutput) == "" {
		return errors.New("usage: job-agent auth import --source export.json --state-output ./data/profiles/primary.json [--force]")
	}
	if _, err := os.Lstat(*stateOutput); err == nil && !*force {
		return fmt.Errorf("state output %s already exists; pass --force to replace it", *stateOutput)
	}
	data, err := os.ReadFile(*source)
	if err != nil {
		return fmt.Errorf("read storage state: %w", err)
	}
	sanitized, result, err := hh.SanitizeBrowserStorageStateData(data)
	if err != nil {
		return err
	}
	if err := writePrivateFile(*stateOutput, sanitized); err != nil {
		return err
	}
	fmt.Fprintf(output, "IMPORTED cookies=%d origins=%d output=%s\n", result.CookiesAfter, result.OriginsAfter, *stateOutput)
	return nil
}

func writePrivateFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create state output: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect state output: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write state output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("flush state output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close state output: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace state output: %w", err)
	}
	return nil
}

type authSessionRequest struct {
	Platform              string `json:"platform"`
	ProfileID             string `json:"profile_id"`
	CredentialReference   string `json:"credential_reference,omitempty"`
	BrowserStateReference string `json:"browser_state_reference,omitempty"`
	TTL                   string `json:"ttl,omitempty"`
}

type authInputRequest struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

func runAuthLogin(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	return runAuthLoginWith(ctx, args, output, client, promptTTY)
}

type authPrompter func(label string) (string, error)

func runAuthLoginWith(ctx context.Context, args []string, output io.Writer, client *http.Client, prompt authPrompter) error {
	flags := flag.NewFlagSet("job-agent auth login", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	platform := flags.String("platform", "hh", "platform tag")
	profile := flags.String("profile", "", "profile tag")
	stateOutput := flags.String("state-output", "", "path for the sanitized Playwright storage state")
	credentialOutput := flags.String("credential-output", "", "credential output path or '-' for stdout")
	credentialFormat := flags.String("credential-format", "json", "json or dotenv")
	imageProtocol := flags.String("image-protocol", "auto", "auto, kitty, sixel, unicode or file")
	presenterMode := flags.String("presenter", "auto", "auto, terminal, dashboard or vnc")
	force := flags.Bool("force", false, "replace an existing credential output")
	ttl := flags.String("ttl", "", "session ttl, for example 15m")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*profile) == "" {
		return errors.New("usage: job-agent auth login --profile <tag> [--state-output file] [--credential-output file]")
	}
	if strings.TrimSpace(*stateOutput) == "" && strings.TrimSpace(*credentialOutput) == "" {
		return errors.New("auth login requires --state-output and/or --credential-output")
	}
	mode := strings.ToLower(strings.TrimSpace(*presenterMode))
	if mode != "" && mode != "auto" && mode != "terminal" {
		return fmt.Errorf("presenter %q is not supported yet; use terminal", *presenterMode)
	}
	protocol, err := presenter.ParseProtocol(*imageProtocol)
	if err != nil {
		return err
	}
	credentialReference := ""
	if output := strings.TrimSpace(*credentialOutput); output != "" {
		if output == "-" {
			return errors.New("--credential-output - requires OAuth token exchange, which browser login does not produce yet")
		}
		format, err := credentials.ParseFormat(*credentialFormat)
		if err != nil {
			return err
		}
		if _, err := os.Stat(output); err == nil && !*force {
			return fmt.Errorf("credential output %s already exists; pass --force to replace it", output)
		}
		if format == "dotenv" {
			credentialReference = "dotenv-file:" + output
		} else {
			credentialReference = "file:" + output
		}
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
	var session core.AuthSession
	request := authSessionRequest{
		Platform: strings.TrimSpace(*platform), ProfileID: strings.TrimSpace(*profile),
		CredentialReference: credentialReference, BrowserStateReference: strings.TrimSpace(*stateOutput),
		TTL: strings.TrimSpace(*ttl),
	}
	if err := authJSON(ctx, client, http.MethodPost, base+"/api/v1/auth/sessions", request, &session); err != nil {
		return fmt.Errorf("create auth session: %w", err)
	}
	fmt.Fprintf(output, "SESSION id=%s status=%s\n", session.ID, session.Status)
	for {
		switch session.Status {
		case core.AuthSessionCompleted:
			fmt.Fprintf(output, "DONE status=%s credential=%s browser_state=%s\n",
				session.Status, referenceOrNone(session.CredentialReference), referenceOrNone(session.BrowserStateReference))
			return nil
		case core.AuthSessionFailed, core.AuthSessionExpired, core.AuthSessionCancelled:
			return fmt.Errorf("auth session %s: %s %s", session.Status, session.FailureCategory, session.FailureMessage)
		case core.AuthSessionWaitingIdentifier:
			value, err := prompt("Email HH: ")
			if err != nil {
				return err
			}
			if err := authJSON(ctx, client, http.MethodPost, sessionInputURL(base, session.ID), authInputRequest{Kind: string(auth.InputIdentifier), Value: value}, &session); err != nil {
				return err
			}
		case core.AuthSessionWaitingOTP:
			value, err := prompt("Код из письма или SMS: ")
			if err != nil {
				return err
			}
			if err := authJSON(ctx, client, http.MethodPost, sessionInputURL(base, session.ID), authInputRequest{Kind: string(auth.InputOTP), Value: value}, &session); err != nil {
				return err
			}
		case core.AuthSessionWaitingPassword:
			return errors.New("HH asked for a password; password login is not supported")
		case core.AuthSessionWaitingCaptcha:
			payload, err := authChallenge(ctx, client, base, session.ID)
			if err != nil {
				return err
			}
			if err := renderChallenge(payload, protocol); err != nil {
				return err
			}
			value, err := prompt("Символы с картинки: ")
			if err != nil {
				return err
			}
			if err := authJSON(ctx, client, http.MethodPost, sessionInputURL(base, session.ID), authInputRequest{Kind: string(auth.InputCaptcha), Value: value}, &session); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected auth session status %q", session.Status)
		}
	}
}

func runAuthStatus(ctx context.Context, args []string, output io.Writer, client *http.Client) error {
	flags := flag.NewFlagSet("job-agent auth status", flag.ContinueOnError)
	flags.SetOutput(output)
	apiURL := flags.String("api", "http://127.0.0.1:8080", "job-agent backend URL")
	sessionID := flags.String("session", "", "auth session id")
	watch := flags.Bool("watch", false, "follow session transitions until terminal status")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*sessionID) == "" {
		return errors.New("usage: job-agent auth status --session <id> [--watch]")
	}
	base, err := authBaseURL(*apiURL)
	if err != nil {
		return err
	}
	if *watch {
		return watchAuthSession(ctx, client, base, strings.TrimSpace(*sessionID), output)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	var session core.AuthSession
	if err := authJSON(ctx, client, http.MethodGet, base+"/api/v1/auth/sessions/"+url.PathEscape(*sessionID), nil, &session); err != nil {
		return err
	}
	fmt.Fprintf(output, "id=%s profile=%s status=%s revision=%d\n",
		session.ID, session.ProfileID, session.Status, session.Revision)
	if session.Challenge != nil {
		fmt.Fprintf(output, "challenge kind=%s deadline=%s\n", session.Challenge.Kind, session.Challenge.Deadline.Format(time.RFC3339))
	}
	if session.FailureCategory != "" {
		fmt.Fprintf(output, "failure=%s %s\n", session.FailureCategory, session.FailureMessage)
	}
	return nil
}

// watchAuthSession follows the SSE stream of one session. It prints every
// revision and returns when the session reaches a terminal status.
func watchAuthSession(ctx context.Context, client *http.Client, base, sessionID string, output io.Writer) error {
	if client == nil {
		client = &http.Client{}
	}
	endpoint := base + "/api/v1/auth/sessions/" + url.PathEscape(sessionID) + "/events"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, maximumAuthJSONResponse))
		return authResponseError(response.StatusCode, data)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var data string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && data != "":
			var session core.AuthSession
			if err := json.Unmarshal([]byte(data), &session); err != nil {
				return fmt.Errorf("decode auth event: %w", err)
			}
			fmt.Fprintf(output, "id=%s profile=%s status=%s revision=%d\n",
				session.ID, session.ProfileID, session.Status, session.Revision)
			if session.FailureCategory != "" {
				fmt.Fprintf(output, "failure=%s %s\n", session.FailureCategory, session.FailureMessage)
			}
			if authTerminalStatus(session.Status) {
				return nil
			}
			data = ""
		}
	}
	return scanner.Err()
}

func authTerminalStatus(status core.AuthSessionStatus) bool {
	switch status {
	case core.AuthSessionCompleted, core.AuthSessionExpired, core.AuthSessionCancelled, core.AuthSessionFailed:
		return true
	default:
		return false
	}
}

func authBaseURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(value), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("auth --api must be an absolute http or https URL")
	}
	return parsed.String(), nil
}

func sessionInputURL(base string, id core.AuthSessionID) string {
	return base + "/api/v1/auth/sessions/" + url.PathEscape(string(id)) + "/inputs"
}

func checkAuthAPIVersion(ctx context.Context, client *http.Client, base string) error {
	var version struct {
		APIVersion string `json:"api_version"`
	}
	if err := authJSON(ctx, client, http.MethodGet, base+"/api/v1/version", nil, &version); err != nil {
		return fmt.Errorf("check API version: %w", err)
	}
	if version.APIVersion != "" && version.APIVersion != "v1" {
		return fmt.Errorf("backend API version %q is not supported", version.APIVersion)
	}
	return nil
}

func authChallenge(ctx context.Context, client *http.Client, base string, id core.AuthSessionID) ([]byte, error) {
	endpoint := base + "/api/v1/auth/sessions/" + url.PathEscape(string(id)) + "/challenge"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumAuthChallenge+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maximumAuthChallenge {
		return nil, errors.New("auth challenge is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, authResponseError(response.StatusCode, body)
	}
	return body, nil
}

func authJSON(ctx context.Context, client *http.Client, method, endpoint string, body, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumAuthJSONResponse+1))
	if err != nil {
		return err
	}
	if len(data) > maximumAuthJSONResponse {
		return errors.New("auth response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return authResponseError(response.StatusCode, data)
	}
	if target == nil {
		return nil
	}
	return json.Unmarshal(data, target)
}

func authResponseError(status int, body []byte) error {
	var problem struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &problem) == nil && strings.TrimSpace(problem.Error) != "" {
		return fmt.Errorf("backend returned %d: %s", status, problem.Error)
	}
	return fmt.Errorf("backend returned status %d", status)
}

func referenceOrNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func renderChallenge(png []byte, requested presenter.Protocol) error {
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if found {
			environment[key] = value
		}
	}
	protocol := presenter.DetectProtocol(requested, environment)
	switch protocol {
	case presenter.ProtocolKitty:
		rendered, err := presenter.RenderKitty(png)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stderr, rendered)
	case presenter.ProtocolSixel:
		rendered, err := presenter.RenderSixel(png)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stderr, rendered)
	case presenter.ProtocolUnicode:
		rendered, err := presenter.RenderUnicode(png)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stderr, rendered)
	default:
		path, err := presenter.RenderFile(png)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "challenge image: %s\n", path)
	}
	return nil
}

// promptTTY reads one line from the controlling terminal so that stdout stays
// clean for credential or state output.
func promptTTY(label string) (string, error) {
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		defer tty.Close()
		fmt.Fprint(tty, label)
		line, err := bufio.NewReader(tty).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	fmt.Fprint(os.Stderr, label)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
