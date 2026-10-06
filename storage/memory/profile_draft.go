package memory

import (
	"context"
	"errors"
	"sort"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func (repository *Repository) CreateProfileDraft(ctx context.Context, candidate core.ProfileDraft) (core.ProfileDraft, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.ProfileDraft{}, false, err
	}
	if err := candidate.Validate(); err != nil {
		return core.ProfileDraft{}, false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if stored, exists := repository.profileDrafts[candidate.Tag]; exists {
		return cloneProfileDraft(stored), false, nil
	}
	repository.profileDrafts[candidate.Tag] = cloneProfileDraft(candidate)
	return cloneProfileDraft(candidate), true, nil
}

func (repository *Repository) ProfileDraft(ctx context.Context, tag string) (core.ProfileDraft, error) {
	if err := ctx.Err(); err != nil {
		return core.ProfileDraft{}, err
	}
	if tag == "" {
		return core.ProfileDraft{}, errors.New("profile draft requires tag")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	draft, exists := repository.profileDrafts[tag]
	if !exists {
		return core.ProfileDraft{}, storage.ErrProfileDraftNotFound
	}
	return cloneProfileDraft(draft), nil
}

func (repository *Repository) ProfileDrafts(ctx context.Context) ([]core.ProfileDraft, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	drafts := make([]core.ProfileDraft, 0, len(repository.profileDrafts))
	for _, draft := range repository.profileDrafts {
		drafts = append(drafts, cloneProfileDraft(draft))
	}
	sort.Slice(drafts, func(first, second int) bool {
		if !drafts[first].CreatedAt.Equal(drafts[second].CreatedAt) {
			return drafts[first].CreatedAt.Before(drafts[second].CreatedAt)
		}
		return drafts[first].Tag < drafts[second].Tag
	})
	return drafts, nil
}

func (repository *Repository) SaveProfileDraft(ctx context.Context, candidate core.ProfileDraft, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.profileDrafts[candidate.Tag]
	if !exists {
		return storage.ErrProfileDraftNotFound
	}
	if stored.Revision != expectedRevision {
		return storage.ErrRevisionConflict
	}
	repository.profileDrafts[candidate.Tag] = cloneProfileDraft(candidate)
	return nil
}

func (repository *Repository) DeleteProfileDraft(ctx context.Context, tag string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.profileDrafts[tag]; !exists {
		return storage.ErrProfileDraftNotFound
	}
	delete(repository.profileDrafts, tag)
	return nil
}

func cloneProfileDraft(draft core.ProfileDraft) core.ProfileDraft {
	cloned := draft
	if draft.Identity != nil {
		identity := *draft.Identity
		cloned.Identity = &identity
	}
	cloned.Resumes = append([]core.ProfileDraftResume(nil), draft.Resumes...)
	return cloned
}
