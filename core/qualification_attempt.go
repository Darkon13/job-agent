package core

import (
	"errors"
	"fmt"
)

// QualificationAttempt is one completed attempt of a platform qualification
// level. History keeps every attempt; the best result is promoted monotonically
// with PreferQualificationResult.
type QualificationAttempt struct {
	Platform           Platform
	ProfileID          ProfileID
	Qualification      QualificationDescriptor
	Result             QualificationResult
	AttemptFingerprint string
}

func (attempt QualificationAttempt) Validate() error {
	if attempt.Platform == "" || attempt.ProfileID == "" {
		return errors.New("qualification attempt requires platform and profile")
	}
	if err := validateQualificationDescriptor(attempt.Qualification); err != nil {
		return fmt.Errorf("qualification attempt: %w", err)
	}
	if err := attempt.Result.Validate(); err != nil {
		return fmt.Errorf("qualification attempt: %w", err)
	}
	if attempt.AttemptFingerprint != "" {
		if err := validateSHA256Fingerprint(attempt.AttemptFingerprint); err != nil {
			return fmt.Errorf("qualification attempt fingerprint: %w", err)
		}
	}
	return nil
}
