package memory

import (
	"bytes"
	"context"
	"errors"
	"slices"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func (repository *Repository) CreateApplicationTailoring(ctx context.Context, candidate core.ApplicationTailoring) (core.ApplicationTailoring, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.ApplicationTailoring{}, false, err
	}
	if err := candidate.Validate(); err != nil {
		return core.ApplicationTailoring{}, false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if stored, exists := repository.applicationTailorings[candidate.ID]; exists {
		if !sameApplicationTailoringInputs(stored, candidate) {
			return core.ApplicationTailoring{}, false, errors.New("application tailoring id conflicts with different contents")
		}
		return cloneApplicationTailoring(stored), false, nil
	}
	if id, exists := repository.tailoringApplications[candidate.ApplicationID]; exists {
		stored := repository.applicationTailorings[id]
		if !sameApplicationTailoringInputs(stored, candidate) {
			return core.ApplicationTailoring{}, false, errors.New("application already has a different tailoring workflow")
		}
		return cloneApplicationTailoring(stored), false, nil
	}
	if id, locked := repository.tailoringProfiles[candidate.Key.ProfileID]; locked {
		return core.ApplicationTailoring{}, false, errors.Join(storage.ErrProfileMutationLocked, errors.New(string(id)))
	}
	repository.applicationTailorings[candidate.ID] = cloneApplicationTailoring(candidate)
	repository.tailoringApplications[candidate.ApplicationID] = candidate.ID
	repository.tailoringProfiles[candidate.Key.ProfileID] = candidate.ID
	return cloneApplicationTailoring(candidate), true, nil
}

func (repository *Repository) ApplicationTailoring(ctx context.Context, id core.ApplicationTailoringID) (core.ApplicationTailoring, error) {
	if err := ctx.Err(); err != nil {
		return core.ApplicationTailoring{}, err
	}
	if id == "" {
		return core.ApplicationTailoring{}, errors.New("application tailoring requires id")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	tailoring, exists := repository.applicationTailorings[id]
	if !exists {
		return core.ApplicationTailoring{}, storage.ErrApplicationTailoringNotFound
	}
	return cloneApplicationTailoring(tailoring), nil
}

func (repository *Repository) ApplicationTailoringByApplication(ctx context.Context, applicationID core.ApplicationID) (core.ApplicationTailoring, error) {
	if err := ctx.Err(); err != nil {
		return core.ApplicationTailoring{}, err
	}
	if applicationID == "" {
		return core.ApplicationTailoring{}, errors.New("application tailoring requires application id")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	id, exists := repository.tailoringApplications[applicationID]
	if !exists {
		return core.ApplicationTailoring{}, storage.ErrApplicationTailoringNotFound
	}
	return cloneApplicationTailoring(repository.applicationTailorings[id]), nil
}

func (repository *Repository) SaveApplicationTailoring(ctx context.Context, candidate core.ApplicationTailoring, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.applicationTailorings[candidate.ID]
	if !exists {
		return storage.ErrApplicationTailoringNotFound
	}
	if stored.Revision != expectedRevision || candidate.Revision != expectedRevision+1 {
		return storage.ErrRevisionConflict
	}
	if !sameApplicationTailoringInputs(stored, candidate) {
		return errors.New("application tailoring immutable inputs changed")
	}
	repository.applicationTailorings[candidate.ID] = cloneApplicationTailoring(candidate)
	if candidate.Status == core.ApplicationTailoringRestored {
		if repository.tailoringProfiles[candidate.Key.ProfileID] == candidate.ID {
			delete(repository.tailoringProfiles, candidate.Key.ProfileID)
		}
		if repository.tailoringApplications[candidate.ApplicationID] == candidate.ID {
			delete(repository.tailoringApplications, candidate.ApplicationID)
		}
	} else {
		repository.tailoringProfiles[candidate.Key.ProfileID] = candidate.ID
	}
	return nil
}

func sameApplicationTailoringInputs(left, right core.ApplicationTailoring) bool {
	return left.ApplicationID == right.ApplicationID && left.Attempt == right.Attempt && left.Key == right.Key &&
		left.ResumeID == right.ResumeID && left.IdempotencyKey == right.IdempotencyKey &&
		left.ProcessorTag == right.ProcessorTag && left.ProcessorVersion == right.ProcessorVersion &&
		left.ProcessorInputDigest == right.ProcessorInputDigest && slices.Equal(left.AllowedPaths, right.AllowedPaths) &&
		left.BaselineDigest == right.BaselineDigest && left.TailoredDigest == right.TailoredDigest &&
		left.BaselineRemoteRevision == right.BaselineRemoteRevision &&
		left.BaselineObservedAt.Equal(right.BaselineObservedAt) &&
		bytes.Equal(left.BaselineState, right.BaselineState) && bytes.Equal(left.TailoredState, right.TailoredState) &&
		left.CreatedAt.Equal(right.CreatedAt)
}

func cloneApplicationTailoring(tailoring core.ApplicationTailoring) core.ApplicationTailoring {
	tailoring.AllowedPaths = slices.Clone(tailoring.AllowedPaths)
	tailoring.BaselineState = append([]byte(nil), tailoring.BaselineState...)
	tailoring.TailoredState = append([]byte(nil), tailoring.TailoredState...)
	return tailoring
}
