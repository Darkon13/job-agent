package core

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type TestAttemptStatus string

const (
	// TestAttemptSubmitted means the platform accepted the answers but the
	// graded outcome is not observed yet. The application flow may continue.
	TestAttemptSubmitted TestAttemptStatus = "submitted"
	TestAttemptPassed    TestAttemptStatus = "passed"
	TestAttemptFailed    TestAttemptStatus = "failed"
)

// TestAttempt is the latest observed outcome of a vacancy test for one profile.
// It gates application submission: a passed attempt lets the pipeline continue,
// while a failed one requires an explicit operator decision.
type TestAttempt struct {
	Platform           Platform          `json:"platform"`
	ProfileID          ProfileID         `json:"profile_id"`
	ExternalID         string            `json:"external_id"`
	TestDefinitionID   TestDefinitionID  `json:"test_definition_id"`
	Status             TestAttemptStatus `json:"status"`
	AttemptFingerprint string            `json:"attempt_fingerprint,omitempty"`
	Attempts           uint64            `json:"attempts"`
	ObservedAt         time.Time         `json:"observed_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

func (attempt TestAttempt) Validate() error {
	if attempt.Platform == "" || attempt.ProfileID == "" || strings.TrimSpace(attempt.ExternalID) == "" || attempt.TestDefinitionID == "" {
		return errors.New("test attempt requires platform, profile, external id and definition")
	}
	switch attempt.Status {
	case TestAttemptSubmitted, TestAttemptPassed, TestAttemptFailed:
	default:
		return fmt.Errorf("test attempt has unsupported status %q", attempt.Status)
	}
	if attempt.Attempts == 0 || attempt.ObservedAt.IsZero() || attempt.UpdatedAt.Before(attempt.ObservedAt) {
		return errors.New("test attempt has invalid attempts or timestamps")
	}
	if attempt.AttemptFingerprint != "" {
		if err := validateSHA256Fingerprint(attempt.AttemptFingerprint); err != nil {
			return err
		}
	}
	return nil
}

func (attempt TestAttempt) Passed() bool { return attempt.Status == TestAttemptPassed }

// Continues reports whether the recorded outcome allows the application
// pipeline to proceed. Only an explicit failure blocks it.
func (attempt TestAttempt) Continues() bool { return attempt.Status != TestAttemptFailed }
