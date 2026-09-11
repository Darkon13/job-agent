// Package browser is the typed Go client for the thin Playwright browser
// worker. It owns request/response shapes and error normalization only; page
// selectors stay in adapters.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/core"
)

const ProtocolVersion = "1"

type Health struct {
	Status   string `json:"status"`
	Version  string `json:"version,omitempty"`
	Protocol int    `json:"protocol,omitempty"`
}

type Ready struct {
	Status   string `json:"status"`
	Browser  bool   `json:"browser"`
	Contexts int    `json:"contexts"`
}

type EnsureRequest struct {
	Headless *bool `json:"headless,omitempty"`
}

type ContextInfo struct {
	ProfileID core.ProfileID `json:"profile_id"`
	Created   bool           `json:"created,omitempty"`
	Pages     int            `json:"pages"`
	Headless  bool           `json:"headless"`
}

type GotoRequest struct {
	URL       string `json:"url"`
	WaitUntil string `json:"wait_until,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type GotoResult struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
	Title  string `json:"title"`
}

type PageInfo struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	HasPage bool   `json:"has_page"`
}

type ContentRequest struct {
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

type ContentResult struct {
	HTML string `json:"html"`
	URL  string `json:"url"`
}

type ScreenshotRequest struct {
	Selector  string `json:"selector,omitempty"`
	FullPage  bool   `json:"full_page,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type LocatorRequest struct {
	Action    string `json:"action"`
	Selector  string `json:"selector"`
	Value     string `json:"value,omitempty"`
	State     string `json:"state,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

// Client is the worker surface used by adapters. Implementations must
// serialize operations of one profile.
type Client interface {
	Health(ctx context.Context) (Health, error)
	Ready(ctx context.Context) (Ready, error)

	Ensure(ctx context.Context, profileID core.ProfileID, request EnsureRequest) (ContextInfo, error)
	List(ctx context.Context) ([]ContextInfo, error)
	Close(ctx context.Context, profileID core.ProfileID, purge bool) error

	ExportStorageState(ctx context.Context, profileID core.ProfileID) (json.RawMessage, error)
	ImportStorageState(ctx context.Context, profileID core.ProfileID, state json.RawMessage) error

	Goto(ctx context.Context, profileID core.ProfileID, request GotoRequest) (GotoResult, error)
	Page(ctx context.Context, profileID core.ProfileID) (PageInfo, error)
	Content(ctx context.Context, profileID core.ProfileID, request ContentRequest) (ContentResult, error)
	Screenshot(ctx context.Context, profileID core.ProfileID, request ScreenshotRequest) ([]byte, error)
	Locator(ctx context.Context, profileID core.ProfileID, request LocatorRequest) error
}

// Error is one structured worker failure.
type Error struct {
	StatusCode int
	Code       string
	Message    string
}

func (err *Error) Error() string {
	if err == nil {
		return "<nil>"
	}
	if err.Message == "" {
		return err.Code
	}
	return err.Code + ": " + err.Message
}

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// OperationError normalizes a worker or transport failure into a core
// operation error so callers can reuse retry and fallback policy.
func OperationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var workerError *Error
	if errors.As(err, &workerError) {
		return &core.OperationError{
			Category: categoryForWorkerError(workerError), Operation: operation,
			Message: workerError.Message, Cause: workerError,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &core.OperationError{
			Category: core.ErrorTemporaryFailure, Operation: operation,
			Message: "browser worker request was interrupted", Cause: err,
		}
	}
	return &core.OperationError{
		Category: core.ErrorTemporaryFailure, Operation: operation,
		Message: "browser worker is unavailable", Cause: err,
	}
}

func categoryForWorkerError(err *Error) core.ErrorCategory {
	switch err.Code {
	case "unauthorized":
		return core.ErrorUnauthorized
	case "busy":
		return core.ErrorConflict
	case "timeout":
		return core.ErrorTemporaryFailure
	case "unsupported":
		return core.ErrorUnsupported
	case "invalid", "not_found", "protocol":
		return core.ErrorPermanentFailure
	default:
		if err.StatusCode >= 500 {
			return core.ErrorTemporaryFailure
		}
		return core.ErrorPermanentFailure
	}
}

func invalidResponse(format string, args ...any) error {
	return &Error{Code: "invalid", Message: fmt.Sprintf(format, args...)}
}
