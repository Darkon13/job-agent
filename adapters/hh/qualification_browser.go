package hh

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/Darkon13/job-agent/core"
)

type QualificationOffering struct {
	SkillID        string `json:"skill_id"`
	FamilyName     string `json:"family_name"`
	Level          string `json:"level"`
	Kind           string `json:"kind"`
	URL            string `json:"url"`
	Summary        string `json:"summary"`
	StartAvailable bool   `json:"start_available"`
}

type QualificationProgress struct {
	Current int `json:"current"`
	Total   int `json:"total"`
}

type QualificationCapture struct {
	Status            string                `json:"status"`
	URL               string                `json:"url"`
	Title             string                `json:"title"`
	Question          core.Question         `json:"question"`
	SelectedOptionIDs []string              `json:"selected_option_ids"`
	Progress          QualificationProgress `json:"progress"`
	TimeLeftSeconds   int                   `json:"time_left_seconds"`
}

func (capture QualificationCapture) Completed() bool { return capture.Status == "completed" }

type QualificationBrowserOptions struct {
	NodePath    string
	WorkerPath  string
	BrowserPath string
	StateFile   string
	Proxy       string
	Headless    bool
	Stderr      io.Writer
}

// QualificationBrowser is a narrow JSON-lines bridge to the Playwright
// worker. Runtime option IDs stay inside this process boundary and are never
// used as stable answer keys.
type QualificationBrowser struct {
	mu      sync.Mutex
	command *exec.Cmd
	input   io.WriteCloser
	output  *bufio.Scanner
	nextID  uint64
	closed  bool
}

func StartQualificationBrowser(ctx context.Context, options QualificationBrowserOptions) (*QualificationBrowser, error) {
	if ctx == nil {
		return nil, errors.New("qualification browser requires context")
	}
	if strings.TrimSpace(options.NodePath) == "" || strings.TrimSpace(options.WorkerPath) == "" ||
		strings.TrimSpace(options.BrowserPath) == "" || strings.TrimSpace(options.StateFile) == "" {
		return nil, errors.New("qualification browser requires node, worker, browser and state-file paths")
	}
	args := []string{options.WorkerPath, "--browser", options.BrowserPath, "--state-file", options.StateFile}
	if options.Proxy != "" {
		args = append(args, "--proxy", options.Proxy)
	}
	if options.Headless {
		args = append(args, "--headless")
	}
	command := exec.CommandContext(ctx, options.NodePath, args...)
	input, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("qualification browser stdin: %w", err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, fmt.Errorf("qualification browser stdout: %w", err)
	}
	command.Stderr = options.Stderr
	if err := command.Start(); err != nil {
		_ = input.Close()
		return nil, fmt.Errorf("start qualification browser: %w", err)
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	return &QualificationBrowser{command: command, input: input, output: scanner}, nil
}

func (browser *QualificationBrowser) OpenOffering(ctx context.Context, skillID, level, kind string) (QualificationOffering, error) {
	var response qualificationBrowserResponse
	err := browser.call(ctx, qualificationBrowserCommand{Type: "open", SkillID: skillID, Level: level, Kind: kind}, &response)
	return response.Offering, err
}

func (browser *QualificationBrowser) StartAttempt(ctx context.Context) (QualificationCapture, error) {
	var response qualificationBrowserResponse
	err := browser.call(ctx, qualificationBrowserCommand{Type: "start"}, &response)
	return response.Capture, err
}

func (browser *QualificationBrowser) Capture(ctx context.Context) (QualificationCapture, error) {
	var response qualificationBrowserResponse
	err := browser.call(ctx, qualificationBrowserCommand{Type: "capture"}, &response)
	return response.Capture, err
}

func (browser *QualificationBrowser) Select(ctx context.Context, expectedQuestion string, optionIDs []string) (QualificationCapture, error) {
	var response qualificationBrowserResponse
	err := browser.call(ctx, qualificationBrowserCommand{
		Type: "select", ExpectedQuestion: expectedQuestion, OptionIDs: append([]string(nil), optionIDs...),
	}, &response)
	return response.Capture, err
}

func (browser *QualificationBrowser) Next(ctx context.Context, expectedQuestion string, optionIDs []string) (QualificationCapture, error) {
	var response qualificationBrowserResponse
	err := browser.call(ctx, qualificationBrowserCommand{
		Type: "next", ExpectedQuestion: expectedQuestion, OptionIDs: append([]string(nil), optionIDs...),
	}, &response)
	return response.Capture, err
}

func (browser *QualificationBrowser) Close() error {
	if browser == nil {
		return nil
	}
	browser.mu.Lock()
	defer browser.mu.Unlock()
	if browser.closed {
		return nil
	}
	browser.closed = true
	_ = browser.input.Close()
	if err := browser.command.Wait(); err != nil {
		return fmt.Errorf("qualification browser exit: %w", err)
	}
	return nil
}

type qualificationBrowserCommand struct {
	ID               uint64   `json:"id"`
	Type             string   `json:"type"`
	SkillID          string   `json:"skill_id,omitempty"`
	Level            string   `json:"level,omitempty"`
	Kind             string   `json:"kind,omitempty"`
	ExpectedQuestion string   `json:"expected_question,omitempty"`
	OptionIDs        []string `json:"option_ids,omitempty"`
}

type qualificationBrowserResponse struct {
	ID       uint64                `json:"id"`
	OK       bool                  `json:"ok"`
	Error    string                `json:"error,omitempty"`
	Offering QualificationOffering `json:"offering"`
	Capture  QualificationCapture  `json:"capture"`
}

func (browser *QualificationBrowser) call(ctx context.Context, command qualificationBrowserCommand, response *qualificationBrowserResponse) error {
	if browser == nil {
		return errors.New("qualification browser is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	browser.mu.Lock()
	defer browser.mu.Unlock()
	if browser.closed {
		return errors.New("qualification browser is closed")
	}
	browser.nextID++
	command.ID = browser.nextID
	payload, err := json.Marshal(command)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(browser.input, "%s\n", payload); err != nil {
		return fmt.Errorf("send qualification browser command: %w", err)
	}
	if !browser.output.Scan() {
		if err := browser.output.Err(); err != nil {
			return fmt.Errorf("read qualification browser response: %w", err)
		}
		return errors.New("qualification browser exited before responding")
	}
	if err := json.Unmarshal(browser.output.Bytes(), response); err != nil {
		return fmt.Errorf("decode qualification browser response: %w", err)
	}
	if response.ID != command.ID {
		return fmt.Errorf("qualification browser response id mismatch: got %d, want %d", response.ID, command.ID)
	}
	if !response.OK {
		if response.Error == "" {
			response.Error = "unknown browser error"
		}
		return errors.New(response.Error)
	}
	return nil
}
