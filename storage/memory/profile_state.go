package memory

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sort"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func (repository *Repository) CreateProfileStateProposal(ctx context.Context, candidate core.ProfileStateProposal) (core.ProfileStateProposal, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.ProfileStateProposal{}, false, err
	}
	if err := candidate.Validate(); err != nil {
		return core.ProfileStateProposal{}, false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if stored, exists := repository.profileStateProposals[candidate.ID]; exists {
		if !sameProfileStateProposal(stored, candidate) {
			return core.ProfileStateProposal{}, false, errors.New("profile state proposal id conflicts with different contents")
		}
		return cloneProfileStateProposal(stored), false, nil
	}
	if id, exists := repository.profileStateKeys[candidate.IdempotencyKey]; exists {
		stored := repository.profileStateProposals[id]
		if !sameProfileStateProposalInputs(stored, candidate) {
			return core.ProfileStateProposal{}, false, errors.New("profile state proposal idempotency key conflicts with different inputs")
		}
		return cloneProfileStateProposal(stored), false, nil
	}
	repository.profileStateProposals[candidate.ID] = cloneProfileStateProposal(candidate)
	repository.profileStateKeys[candidate.IdempotencyKey] = candidate.ID
	return cloneProfileStateProposal(candidate), true, nil
}

func (repository *Repository) ProfileStateProposal(ctx context.Context, id core.ProfileStateProposalID) (core.ProfileStateProposal, error) {
	if err := ctx.Err(); err != nil {
		return core.ProfileStateProposal{}, err
	}
	if id == "" {
		return core.ProfileStateProposal{}, errors.New("profile state proposal requires id")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	proposal, exists := repository.profileStateProposals[id]
	if !exists {
		return core.ProfileStateProposal{}, errors.New("profile state proposal not found")
	}
	return cloneProfileStateProposal(proposal), nil
}

func (repository *Repository) ListProfileStateProposals(ctx context.Context, filter storage.ProfileStateProposalFilter) ([]core.ProfileStateProposal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.ProfileStateProposal, 0)
	for _, proposal := range repository.profileStateProposals {
		if filter.ResourceTag != "" && proposal.ResourceTag != filter.ResourceTag ||
			filter.ProfileID != "" && proposal.ProfileID != filter.ProfileID ||
			filter.Status != "" && proposal.Status != filter.Status {
			continue
		}
		result = append(result, cloneProfileStateProposal(proposal))
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.After(result[j].CreatedAt)
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func sameProfileStateProposal(left, right core.ProfileStateProposal) bool {
	return left.ID == right.ID && sameProfileStateProposalInputs(left, right)
}

func sameProfileStateProposalInputs(left, right core.ProfileStateProposal) bool {
	return left.ResourceTag == right.ResourceTag && left.ProfileID == right.ProfileID && left.Ownership == right.Ownership &&
		left.Status == right.Status && left.IdempotencyKey == right.IdempotencyKey && left.ManifestDigest == right.ManifestDigest &&
		left.ObservedDigest == right.ObservedDigest && left.DesiredDigest == right.DesiredDigest && left.RemoteRevision == right.RemoteRevision &&
		bytes.Equal(left.DesiredState, right.DesiredState) && slices.Equal(left.Changes, right.Changes)
}

func cloneProfileStateProposal(proposal core.ProfileStateProposal) core.ProfileStateProposal {
	proposal.DesiredState = append([]byte(nil), proposal.DesiredState...)
	proposal.Changes = slices.Clone(proposal.Changes)
	return proposal
}

func (repository *Repository) CreateProfileStateRevision(ctx context.Context, candidate core.ProfileStateRevision) (core.ProfileStateRevision, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.ProfileStateRevision{}, false, err
	}
	if err := candidate.Validate(); err != nil {
		return core.ProfileStateRevision{}, false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if stored, exists := repository.profileStateRevisions[candidate.ProposalID]; exists {
		if !sameProfileStateRevisionInputs(stored, candidate) {
			return core.ProfileStateRevision{}, false, errors.New("profile state revision conflicts with existing contents")
		}
		return cloneProfileStateRevision(stored), false, nil
	}
	repository.profileStateRevisions[candidate.ProposalID] = cloneProfileStateRevision(candidate)
	return cloneProfileStateRevision(candidate), true, nil
}

func (repository *Repository) ListProfileStateRevisions(ctx context.Context, filter storage.ProfileStateRevisionFilter) ([]core.ProfileStateRevision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.ProfileStateRevision, 0)
	for _, revision := range repository.profileStateRevisions {
		if filter.ResourceTag != "" && revision.ResourceTag != filter.ResourceTag ||
			filter.ProfileID != "" && revision.ProfileID != filter.ProfileID {
			continue
		}
		result = append(result, cloneProfileStateRevision(revision))
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].AppliedAt.Equal(result[j].AppliedAt) {
			return result[i].AppliedAt.After(result[j].AppliedAt)
		}
		return result[i].ProposalID < result[j].ProposalID
	})
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

// sameProfileStateRevisionInputs treats the applied time as non-conflicting
// metadata: a retried recording keeps the first confirmed timestamp.
func sameProfileStateRevisionInputs(left, right core.ProfileStateRevision) bool {
	return left.ResourceTag == right.ResourceTag && left.ProfileID == right.ProfileID &&
		left.ManifestDigest == right.ManifestDigest && left.ObservedDigest == right.ObservedDigest &&
		left.DesiredDigest == right.DesiredDigest && left.RemoteRevision == right.RemoteRevision &&
		left.Source == right.Source && slices.Equal(left.Changes, right.Changes)
}

func cloneProfileStateRevision(revision core.ProfileStateRevision) core.ProfileStateRevision {
	revision.Changes = slices.Clone(revision.Changes)
	return revision
}
