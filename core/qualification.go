package core

import (
	"errors"
	"fmt"
	"time"
)

type QualificationStatus string

const (
	QualificationAvailable   QualificationStatus = "available"
	QualificationInProgress  QualificationStatus = "in_progress"
	QualificationPassed      QualificationStatus = "passed"
	QualificationFailed      QualificationStatus = "failed"
	QualificationUnavailable QualificationStatus = "unavailable"
)

// QualificationOffering is one selectable item returned by the platform
// catalog. Several offerings can share a family and differ by level.
type QualificationOffering struct {
	ID            QualificationID         `json:"id"`
	Platform      Platform                `json:"platform"`
	ProfileID     ProfileID               `json:"profile_id"`
	ExternalID    string                  `json:"external_id"`
	Qualification QualificationDescriptor `json:"qualification"`
	Status        QualificationStatus     `json:"status"`
	BestResult    *QualificationResult    `json:"best_result,omitempty"`
	ObservedAt    time.Time               `json:"observed_at"`
}

// QualificationResult is evidence from one completed attempt. Attempt history
// is retained separately; BestResult is promoted monotonically with
// PreferQualificationResult.
type QualificationResult struct {
	Status         QualificationStatus `json:"status"`
	Score          *float64            `json:"score,omitempty"`
	MaxScore       *float64            `json:"max_score,omitempty"`
	Verified       bool                `json:"verified"`
	AnswerBlockTag string              `json:"answer_block_tag,omitempty"`
	CompletedAt    time.Time           `json:"completed_at"`
}

func (offering QualificationOffering) Validate() error {
	if offering.ID == "" || offering.Platform == "" || offering.ProfileID == "" || offering.ExternalID == "" {
		return errors.New("qualification offering requires id, platform, profile and external id")
	}
	if err := validateQualificationDescriptor(offering.Qualification); err != nil {
		return fmt.Errorf("qualification offering: %w", err)
	}
	switch offering.Status {
	case QualificationAvailable, QualificationInProgress, QualificationPassed, QualificationFailed, QualificationUnavailable:
	default:
		return fmt.Errorf("qualification offering has unsupported status %q", offering.Status)
	}
	if offering.ObservedAt.IsZero() {
		return errors.New("qualification offering requires observed_at")
	}
	if offering.BestResult != nil {
		if err := offering.BestResult.Validate(); err != nil {
			return fmt.Errorf("qualification best result: %w", err)
		}
	}
	return nil
}

func (result QualificationResult) Validate() error {
	if result.Status != QualificationPassed && result.Status != QualificationFailed {
		return errors.New("qualification result must be passed or failed")
	}
	if result.CompletedAt.IsZero() {
		return errors.New("qualification result requires completed_at")
	}
	if (result.Score == nil) != (result.MaxScore == nil) {
		return errors.New("qualification score and max_score must be provided together")
	}
	if result.Score != nil && (*result.Score < 0 || *result.MaxScore <= 0 || *result.Score > *result.MaxScore) {
		return errors.New("qualification result has invalid score")
	}
	return nil
}

// Reusable reports whether answers from this result may bypass human review.
// Only a platform/user-verified successful result is reusable.
func (result QualificationResult) Reusable() bool {
	return result.Verified && result.Status == QualificationPassed && result.AnswerBlockTag != ""
}

// Passed reports whether the attempt reached a successful status.
func (result QualificationResult) Passed() bool { return result.Status == QualificationPassed }

// PreferQualificationResult applies a one-way best-result policy. Every
// attempt remains in history, but unverified, failed, or lower-scoring evidence
// cannot replace a verified successful best result.
func PreferQualificationResult(current *QualificationResult, candidate QualificationResult) (QualificationResult, bool, error) {
	if err := candidate.Validate(); err != nil {
		return QualificationResult{}, false, err
	}
	if !candidate.Verified {
		if current == nil {
			return QualificationResult{}, false, nil
		}
		return *current, false, nil
	}
	if current == nil || !current.Verified {
		return candidate, true, nil
	}
	if err := current.Validate(); err != nil {
		return QualificationResult{}, false, fmt.Errorf("current result: %w", err)
	}
	if current.Status == QualificationPassed && candidate.Status != QualificationPassed {
		return *current, false, nil
	}
	if current.Status != QualificationPassed && candidate.Status == QualificationPassed {
		return candidate, true, nil
	}
	if scoreRatio(candidate) > scoreRatio(*current) {
		return candidate, true, nil
	}
	return *current, false, nil
}

func scoreRatio(result QualificationResult) float64 {
	if result.Score == nil || result.MaxScore == nil {
		return -1
	}
	return *result.Score / *result.MaxScore
}
