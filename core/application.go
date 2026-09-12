package core

import (
	"crypto/sha256"
	"encoding/hex"
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
	ID                    ApplicationID                    `json:"id"`
	Key                   ApplicationKey                   `json:"key"`
	Status                ApplicationStatus                `json:"status"`
	Attempts              int                              `json:"attempts"`
	ExternalNegotiationID string                           `json:"external_negotiation_id,omitempty"`
	FailureCategory       ErrorCategory                    `json:"failure_category,omitempty"`
	FailureMessage        string                           `json:"failure_message,omitempty"`
	DecisionCode          string                           `json:"decision_code,omitempty"`
	DecisionReason        string                           `json:"decision_reason,omitempty"`
	PreparedResumeID      string                           `json:"prepared_resume_id,omitempty"`
	PreparedMessage       string                           `json:"prepared_message,omitempty"`
	PreparationProvenance ApplicationPreparationProvenance `json:"preparation_provenance,omitempty"`
	CreatedAt             time.Time                        `json:"created_at"`
	UpdatedAt             time.Time                        `json:"updated_at"`
	PreparedAt            *time.Time                       `json:"prepared_at,omitempty"`
	SubmittedAt           *time.Time                       `json:"submitted_at,omitempty"`
}

const ApplicationPreparationProvenanceVersion = 3

const (
	legacyApplicationPreparationProvenanceVersion   = 1
	evidenceApplicationPreparationProvenanceVersion = 2
)

const (
	ApplicationPreparationSourceStatic        = "static"
	ApplicationPreparationSourceTemplate      = "template"
	ApplicationPreparationSourceMessagePool   = "message_pool"
	ApplicationPreparationSourceModel         = "model"
	ApplicationPreparationSourceModelFallback = "model_fallback"
)

type ApplicationPreparationProvenance struct {
	Version            int    `json:"version,omitempty"`
	Source             string `json:"source,omitempty"`
	OperatorTag        string `json:"operator_tag,omitempty"`
	OperatorVersion    string `json:"operator_version,omitempty"`
	Model              string `json:"model,omitempty"`
	ProviderResponseID string `json:"provider_response_id,omitempty"`
	FailureKind        string `json:"failure_kind,omitempty"`
	FallbackSource     string `json:"fallback_source,omitempty"`
	MessagePoolTag     string `json:"message_pool_tag,omitempty"`
	MessagePoolDigest  string `json:"message_pool_digest,omitempty"`
	TemplateTag        string `json:"template_tag,omitempty"`
	ResumeFactsTag     string `json:"resume_facts_tag,omitempty"`
	ResumeFactsDigest  string `json:"resume_facts_digest,omitempty"`
	InputDigest        string `json:"input_digest,omitempty"`
	OutputDigest       string `json:"output_digest,omitempty"`
	EvidenceDigest     string `json:"evidence_digest,omitempty"`
	EvidenceClaims     int    `json:"evidence_claims,omitempty"`
}

func (provenance ApplicationPreparationProvenance) IsZero() bool {
	return provenance == (ApplicationPreparationProvenance{})
}

func (provenance ApplicationPreparationProvenance) Validate() error {
	if provenance.IsZero() {
		return nil
	}
	if provenance.Version != legacyApplicationPreparationProvenanceVersion &&
		provenance.Version != evidenceApplicationPreparationProvenanceVersion &&
		provenance.Version != ApplicationPreparationProvenanceVersion {
		return fmt.Errorf("application preparation provenance has unsupported version %d", provenance.Version)
	}
	switch provenance.Source {
	case ApplicationPreparationSourceStatic, ApplicationPreparationSourceTemplate:
	case ApplicationPreparationSourceMessagePool:
		if strings.TrimSpace(provenance.MessagePoolTag) == "" || strings.TrimSpace(provenance.TemplateTag) == "" {
			return errors.New("message pool provenance requires pool and template tags")
		}
		if provenance.Version >= 3 && !validApplicationDigest(provenance.MessagePoolDigest) {
			return errors.New("message pool provenance requires content digest")
		}
	case ApplicationPreparationSourceModel:
		if err := provenance.validateModelFields(true); err != nil {
			return err
		}
	case ApplicationPreparationSourceModelFallback:
		if err := provenance.validateModelFields(false); err != nil {
			return err
		}
		if strings.TrimSpace(provenance.FailureKind) == "" {
			return errors.New("model fallback provenance requires failure kind")
		}
		switch provenance.FallbackSource {
		case ApplicationPreparationSourceStatic, ApplicationPreparationSourceTemplate:
		case ApplicationPreparationSourceMessagePool:
			if strings.TrimSpace(provenance.MessagePoolTag) == "" || strings.TrimSpace(provenance.TemplateTag) == "" {
				return errors.New("model fallback message pool provenance requires pool and template tags")
			}
			if provenance.Version >= 3 && !validApplicationDigest(provenance.MessagePoolDigest) {
				return errors.New("model fallback message pool provenance requires content digest")
			}
		default:
			return fmt.Errorf("model fallback provenance has unsupported source %q", provenance.FallbackSource)
		}
	default:
		return fmt.Errorf("application preparation provenance has unsupported source %q", provenance.Source)
	}
	if !validApplicationDigest(provenance.OutputDigest) {
		return errors.New("application preparation provenance requires output digest")
	}
	if provenance.Version < 3 && provenance.MessagePoolDigest != "" {
		return errors.New("application provenance before v3 cannot contain message pool digest")
	}
	if provenance.Version >= 3 && provenance.Source != ApplicationPreparationSourceMessagePool &&
		!(provenance.Source == ApplicationPreparationSourceModelFallback && provenance.FallbackSource == ApplicationPreparationSourceMessagePool) &&
		provenance.MessagePoolDigest != "" {
		return errors.New("only message pool provenance may contain content digest")
	}
	return nil
}

func (provenance ApplicationPreparationProvenance) validateModelFields(requireInput bool) error {
	if strings.TrimSpace(provenance.OperatorTag) == "" || strings.TrimSpace(provenance.OperatorVersion) == "" ||
		strings.TrimSpace(provenance.ResumeFactsTag) == "" || !validApplicationDigest(provenance.ResumeFactsDigest) {
		return errors.New("model provenance requires operator, version and resume facts")
	}
	if requireInput && !validApplicationDigest(provenance.InputDigest) {
		return errors.New("model provenance requires input digest")
	}
	if provenance.InputDigest != "" && !validApplicationDigest(provenance.InputDigest) {
		return errors.New("model provenance contains invalid input digest")
	}
	if provenance.Source == ApplicationPreparationSourceModel && strings.TrimSpace(provenance.Model) == "" {
		return errors.New("model provenance requires resolved model")
	}
	if provenance.Version >= 2 && provenance.Source == ApplicationPreparationSourceModel &&
		(!validApplicationDigest(provenance.EvidenceDigest) || provenance.EvidenceClaims < 1) {
		return errors.New("model provenance requires verified evidence")
	}
	if provenance.Version < 2 && (provenance.EvidenceDigest != "" || provenance.EvidenceClaims != 0) {
		return errors.New("legacy model provenance cannot contain evidence")
	}
	if provenance.Version >= 2 && provenance.Source != ApplicationPreparationSourceModel &&
		(provenance.EvidenceDigest != "" || provenance.EvidenceClaims != 0) {
		return errors.New("only successful model provenance may contain evidence")
	}
	return nil
}

func validApplicationDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == 32
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

// ResetForRetry clears a blocked or failed preparation so the submit workflow
// re-reads the platform state and re-runs preflight. Only an application
// waiting for validation or already failed can be reset; blocked
// questionnaire, test and suitability decisions are never reused as-is.
func (application *Application) ResetForRetry(now time.Time) error {
	if application == nil {
		return errors.New("application is nil")
	}
	switch application.Status {
	case ApplicationWaitingValidation, ApplicationFailed:
	default:
		return fmt.Errorf("application cannot be retried from status %s", application.Status)
	}
	if err := application.Transition(ApplicationReady, now); err != nil {
		return err
	}
	application.DecisionCode = ""
	application.DecisionReason = ""
	application.PreparedResumeID = ""
	application.PreparedMessage = ""
	application.PreparationProvenance = ApplicationPreparationProvenance{}
	application.PreparedAt = nil
	application.FailureCategory = ""
	application.FailureMessage = ""
	return nil
}

// RecordPreparation stores the exact decision input to the unsafe external
// action. A retry reuses PreparedResumeID and PreparedMessage instead of
// consulting mutable configuration or invoking an operator again.
func (application *Application) RecordPreparation(code, reason, resumeID, message string, now time.Time) error {
	return application.RecordPreparationWithProvenance(code, reason, resumeID, message, ApplicationPreparationProvenance{}, now)
}

func (application *Application) RecordPreparationWithProvenance(code, reason, resumeID, message string, provenance ApplicationPreparationProvenance, now time.Time) error {
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
	if err := provenance.Validate(); err != nil {
		return err
	}
	message = strings.TrimSpace(message)
	if !provenance.IsZero() && provenance.OutputDigest != applicationPreparationTextDigest(message) {
		return errors.New("application preparation provenance output digest does not match message")
	}
	if now.IsZero() || now.Before(application.UpdatedAt) {
		return errors.New("application preparation time must not move backwards")
	}
	preparedAt := now
	application.DecisionCode = code
	application.DecisionReason = reason
	application.PreparedResumeID = strings.TrimSpace(resumeID)
	application.PreparedMessage = message
	application.PreparationProvenance = provenance
	application.PreparedAt = &preparedAt
	application.UpdatedAt = now
	return nil
}

func applicationPreparationTextDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
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
		ApplicationFailed:           {ApplicationReady: {}},
	}
	_, exists := allowed[from][to]
	return exists
}
