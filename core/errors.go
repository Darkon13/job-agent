package core

import (
	"errors"
	"fmt"
	"time"
)

type ErrorCategory string

const (
	ErrorUnsupported          ErrorCategory = "unsupported"
	ErrorUnauthorized         ErrorCategory = "unauthorized"
	ErrorRateLimited          ErrorCategory = "rate_limited"
	ErrorQuotaExceeded        ErrorCategory = "quota_exceeded"
	ErrorValidationRequired   ErrorCategory = "validation_required"
	ErrorTemporaryFailure     ErrorCategory = "temporary_failure"
	ErrorPermanentFailure     ErrorCategory = "permanent_failure"
	ErrorConfirmationRequired ErrorCategory = "confirmation_required"
	ErrorAmbiguousResult      ErrorCategory = "ambiguous_result"
	ErrorConflict             ErrorCategory = "conflict"
)

// OperationError is the transport-neutral error returned by adapters and
// workflows. Metadata must not contain credentials or full personal content.
type OperationError struct {
	Category   ErrorCategory     `json:"category"`
	Operation  string            `json:"operation"`
	Platform   Platform          `json:"platform,omitempty"`
	Message    string            `json:"message,omitempty"`
	RetryAfter *time.Time        `json:"retry_after,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Cause      error             `json:"-"`
}

func (err *OperationError) Error() string {
	if err == nil {
		return "<nil>"
	}
	prefix := string(err.Category)
	if err.Operation != "" {
		prefix = err.Operation + ": " + prefix
	}
	if err.Message != "" {
		return prefix + ": " + err.Message
	}
	if err.Cause != nil {
		return prefix + ": " + err.Cause.Error()
	}
	return prefix
}

func (err *OperationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

func (err *OperationError) Validate() error {
	if err == nil {
		return errors.New("operation error is nil")
	}
	switch err.Category {
	case ErrorUnsupported, ErrorUnauthorized, ErrorRateLimited, ErrorQuotaExceeded,
		ErrorValidationRequired, ErrorTemporaryFailure, ErrorPermanentFailure,
		ErrorConfirmationRequired, ErrorAmbiguousResult, ErrorConflict:
	default:
		return fmt.Errorf("unknown error category %q", err.Category)
	}
	if err.Operation == "" {
		return errors.New("operation error requires operation")
	}
	if err.Category != ErrorRateLimited && err.Category != ErrorQuotaExceeded && err.RetryAfter != nil {
		return errors.New("retry_after is only valid for rate_limited or quota_exceeded errors")
	}
	return nil
}

func ErrorIsCategory(err error, category ErrorCategory) bool {
	var operationError *OperationError
	return errors.As(err, &operationError) && operationError.Category == category
}

func AllowsTransportFallback(err error) bool {
	return ErrorIsCategory(err, ErrorUnsupported)
}
