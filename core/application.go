package core

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type ApplicationStatus string

const (
	ApplicationNew               ApplicationStatus = "new"
	ApplicationPreparing         ApplicationStatus = "preparing"
	ApplicationWaitingValidation ApplicationStatus = "waiting_validation"
	ApplicationWaitingApproval   ApplicationStatus = "waiting_approval"
	ApplicationReady             ApplicationStatus = "ready"
	ApplicationSubmitting        ApplicationStatus = "submitting"
	ApplicationPendingReconcile  ApplicationStatus = "pending_reconciliation"
	ApplicationSubmitted         ApplicationStatus = "submitted"
	ApplicationDryRun            ApplicationStatus = "dry_run"
	ApplicationSkipped           ApplicationStatus = "skipped"
	ApplicationFailed            ApplicationStatus = "failed"
)

type Application struct {
	ID                    ApplicationID     `json:"id"`
	Key                   ApplicationKey    `json:"key"`
	Status                ApplicationStatus `json:"status"`
	Attempts              int               `json:"attempts"`
	ExternalNegotiationID string            `json:"external_negotiation_id,omitempty"`
	FailureCategory       ErrorCategory     `json:"failure_category,omitempty"`
	FailureMessage        string            `json:"failure_message,omitempty"`
	DecisionCode          string            `json:"decision_code,omitempty"`
	DecisionReason        string            `json:"decision_reason,omitempty"`
	PreparedMessage       string            `json:"prepared_message,omitempty"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
	PreparedAt            *time.Time        `json:"prepared_at,omitempty"`
	SubmittedAt           *time.Time        `json:"submitted_at,omitempty"`
}

func NewApplication(id ApplicationID, key ApplicationKey, now time.Time) (Application, error) {
	if id == "" {
		return Application{}, errors.New("application requires id")
	}
	if err := key.Validate(); err != nil {
		return Application{}, err
	}
	if now.IsZero() {
		return Application{}, errors.New("application requires current time")
	}
	return Application{ID: id, Key: key, Status: ApplicationNew, CreatedAt: now, UpdatedAt: now}, nil
}

func (application *Application) Transition(to ApplicationStatus, now time.Time) error {
	if application == nil {
		return errors.New("application is nil")
	}
	if now.IsZero() || now.Before(application.UpdatedAt) {
		return errors.New("application transition time must not move backwards")
	}
	if !applicationTransitionAllowed(application.Status, to) {
		return fmt.Errorf("application transition %s -> %s is not allowed", application.Status, to)
	}
	application.Status = to
	application.UpdatedAt = now
	if to == ApplicationSubmitting {
		application.Attempts++
	}
	if to == ApplicationSubmitted {
		submittedAt := now
		application.SubmittedAt = &submittedAt
		application.FailureCategory = ""
		application.FailureMessage = ""
	}
	return nil
}

func (application *Application) Fail(operationError *OperationError, now time.Time) error {
	if operationError == nil {
		return errors.New("application failure requires operation error")
	}
	if err := operationError.Validate(); err != nil {
		return err
	}
	if err := application.Transition(ApplicationFailed, now); err != nil {
		return err
	}
	application.FailureCategory = operationError.Category
	application.FailureMessage = operationError.Message
	return nil
}

// RecordPreparation stores the exact decision input to the unsafe external
// action. A retry reuses PreparedMessage instead of invoking an operator again.
func (application *Application) RecordPreparation(code, reason, message string, now time.Time) error {
	if application == nil {
		return errors.New("application is nil")
	}
	if application.Status != ApplicationPreparing && application.Status != ApplicationReady {
		return fmt.Errorf("application preparation is not allowed in status %s", application.Status)
	}
	code = strings.TrimSpace(code)
	reason = strings.TrimSpace(reason)
	if code == "" || reason == "" {
		return errors.New("application preparation requires decision code and reason")
	}
	if now.IsZero() || now.Before(application.UpdatedAt) {
		return errors.New("application preparation time must not move backwards")
	}
	preparedAt := now
	application.DecisionCode = code
	application.DecisionReason = reason
	application.PreparedMessage = strings.TrimSpace(message)
	application.PreparedAt = &preparedAt
	application.UpdatedAt = now
	return nil
}

func applicationTransitionAllowed(from, to ApplicationStatus) bool {
	allowed := map[ApplicationStatus]map[ApplicationStatus]struct{}{
		ApplicationNew: {
			ApplicationPreparing: {}, ApplicationSkipped: {}, ApplicationFailed: {},
		},
		ApplicationPreparing: {
			ApplicationWaitingValidation: {}, ApplicationWaitingApproval: {}, ApplicationReady: {}, ApplicationDryRun: {}, ApplicationSkipped: {}, ApplicationFailed: {},
		},
		ApplicationWaitingValidation: {
			ApplicationPreparing: {}, ApplicationReady: {}, ApplicationSkipped: {}, ApplicationFailed: {},
		},
		ApplicationWaitingApproval: {
			ApplicationPreparing: {}, ApplicationReady: {}, ApplicationSkipped: {}, ApplicationFailed: {},
		},
		ApplicationReady: {
			ApplicationSubmitting: {}, ApplicationWaitingValidation: {}, ApplicationWaitingApproval: {},
			ApplicationDryRun: {}, ApplicationSkipped: {}, ApplicationFailed: {},
		},
		ApplicationSubmitting: {
			ApplicationSubmitted: {}, ApplicationReady: {}, ApplicationWaitingValidation: {}, ApplicationPendingReconcile: {}, ApplicationFailed: {},
		},
		ApplicationPendingReconcile: {ApplicationSubmitted: {}, ApplicationFailed: {}},
	}
	_, exists := allowed[from][to]
	return exists
}
