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
	if !ValidErrorCategory(err.Category) {
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

// ValidErrorCategory reports whether the value is one of the normalized
// adapter and workflow error categories.
func ValidErrorCategory(category ErrorCategory) bool {
	switch category {
	case ErrorUnsupported, ErrorUnauthorized, ErrorRateLimited, ErrorQuotaExceeded,
		ErrorValidationRequired, ErrorTemporaryFailure, ErrorPermanentFailure,
		ErrorConfirmationRequired, ErrorAmbiguousResult, ErrorConflict:
		return true
	default:
		return false
	}
}

func ErrorIsCategory(err error, category ErrorCategory) bool {
	var operationError *OperationError
	return errors.As(err, &operationError) && operationError.Category == category
}

func AllowsTransportFallback(err error) bool {
	return ErrorIsCategory(err, ErrorUnsupported)
}

// Removal guards are temporary by nature: an application stays linked to a
// running campaign or an unfinished tailoring saga only until that work ends.
// Repositories return these sentinels so workers schedule a retry instead of
// failing the removal permanently.
var (
	ErrApplicationInRunningCampaign   = errors.New("application belongs to a running campaign")
	ErrApplicationActiveTailoringSaga = errors.New("application has an active resume tailoring saga")
)
