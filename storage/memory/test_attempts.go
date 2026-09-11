package memory

import (
	"context"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var _ storage.TestAttemptRepository = (*Repository)(nil)

type testAttemptKey struct {
	platform   core.Platform
	profileID  core.ProfileID
	externalID string
}

func (repository *Repository) SaveTestAttempt(ctx context.Context, attempt core.TestAttempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := attempt.Validate(); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := testAttemptKey{platform: attempt.Platform, profileID: attempt.ProfileID, externalID: attempt.ExternalID}
	stored, exists := repository.testAttempts[key]
	if !exists {
		repository.testAttempts[key] = attempt
		return nil
	}
	merged := attempt
	if stored.Status == core.TestAttemptPassed {
		merged.Status = stored.Status
		merged.AttemptFingerprint = stored.AttemptFingerprint
		merged.ObservedAt = stored.ObservedAt
	}
	if stored.Attempts > merged.Attempts {
		merged.Attempts = stored.Attempts
	}
	if stored.UpdatedAt.After(merged.UpdatedAt) {
		merged.UpdatedAt = stored.UpdatedAt
	}
	if err := merged.Validate(); err != nil {
		return err
	}
	repository.testAttempts[key] = merged
	return nil
}

func (repository *Repository) LatestTestAttempt(ctx context.Context, platform core.Platform, profileID core.ProfileID, externalID string) (core.TestAttempt, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.TestAttempt{}, false, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	attempt, exists := repository.testAttempts[testAttemptKey{platform: platform, profileID: profileID, externalID: externalID}]
	return attempt, exists, nil
}
