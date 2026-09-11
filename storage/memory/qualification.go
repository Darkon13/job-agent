package memory

import (
	"context"
	"errors"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var _ storage.QualificationRepository = (*Repository)(nil)

type qualificationKey struct {
	platform  core.Platform
	profileID core.ProfileID
	familyID  string
	levelID   string
}

func qualificationAttemptKey(attempt core.QualificationAttempt) qualificationKey {
	return qualificationKey{
		platform: attempt.Platform, profileID: attempt.ProfileID,
		familyID: attempt.Qualification.FamilyID, levelID: attempt.Qualification.LevelID,
	}
}

func (repository *Repository) SaveQualificationAttempt(ctx context.Context, attempt core.QualificationAttempt, recordedAt time.Time) (core.QualificationResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.QualificationResult{}, false, err
	}
	if err := attempt.Validate(); err != nil {
		return core.QualificationResult{}, false, err
	}
	if recordedAt.IsZero() {
		return core.QualificationResult{}, false, errors.New("qualification attempt requires recorded_at")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := qualificationAttemptKey(attempt)
	current, exists := repository.qualificationBest[key]
	var currentPtr *core.QualificationResult
	if exists {
		copy := current
		currentPtr = &copy
	}
	best, promoted, err := core.PreferQualificationResult(currentPtr, attempt.Result)
	if err != nil {
		return core.QualificationResult{}, false, err
	}
	repository.qualificationAttempts[key] = append(repository.qualificationAttempts[key], attempt)
	if promoted {
		repository.qualificationBest[key] = best
	}
	if !promoted && !exists {
		return core.QualificationResult{}, false, nil
	}
	return best, promoted, nil
}

func (repository *Repository) BestQualificationResult(ctx context.Context, platform core.Platform, profileID core.ProfileID, familyID, levelID string) (core.QualificationResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.QualificationResult{}, false, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result, exists := repository.qualificationBest[qualificationKey{platform: platform, profileID: profileID, familyID: familyID, levelID: levelID}]
	return result, exists, nil
}

func (repository *Repository) QualificationAttempts(ctx context.Context, platform core.Platform, profileID core.ProfileID, familyID, levelID string) ([]core.QualificationAttempt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	attempts := repository.qualificationAttempts[qualificationKey{platform: platform, profileID: profileID, familyID: familyID, levelID: levelID}]
	result := make([]core.QualificationAttempt, len(attempts))
	for index, attempt := range attempts {
		result[index] = attempt
		if attempt.Qualification.LevelOrder != nil {
			order := *attempt.Qualification.LevelOrder
			result[index].Qualification.LevelOrder = &order
		}
	}
	return result, nil
}
