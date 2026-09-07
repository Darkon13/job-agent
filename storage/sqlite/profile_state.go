package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

const profileStateProposalColumns = `id, resource_tag, profile_id, ownership, status,
	idempotency_key, manifest_digest, observed_digest, desired_digest, remote_revision,
	desired_state, changes, observation_time, revision, created_at, updated_at`

func (store *Store) CreateProfileStateProposal(ctx context.Context, candidate core.ProfileStateProposal) (core.ProfileStateProposal, bool, error) {
	if err := candidate.Validate(); err != nil {
		return core.ProfileStateProposal{}, false, err
	}
	changes, err := json.Marshal(candidate.Changes)
	if err != nil {
		return core.ProfileStateProposal{}, false, fmt.Errorf("encode profile state changes: %w", err)
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO profile_state_proposals (`+profileStateProposalColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidate.ID, candidate.ResourceTag, candidate.ProfileID, candidate.Ownership, candidate.Status,
		candidate.IdempotencyKey, candidate.ManifestDigest, candidate.ObservedDigest, candidate.DesiredDigest,
		candidate.RemoteRevision, []byte(candidate.DesiredState), changes, candidate.ObservationTime.UnixNano(),
		candidate.Revision, candidate.CreatedAt.UnixNano(), candidate.UpdatedAt.UnixNano())
	if err != nil {
		return core.ProfileStateProposal{}, false, fmt.Errorf("insert profile state proposal %s: %w", candidate.ID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil {
		return core.ProfileStateProposal{}, false, err
	}
	if created {
		return cloneSQLiteProfileStateProposal(candidate), true, nil
	}
	stored, err := store.profileStateProposalByIdempotencyKey(ctx, candidate.IdempotencyKey)
	if errors.Is(err, sql.ErrNoRows) {
		stored, err = store.ProfileStateProposal(ctx, candidate.ID)
	}
	if err != nil {
		return core.ProfileStateProposal{}, false, fmt.Errorf("load existing profile state proposal: %w", err)
	}
	if !sameSQLiteProfileStateProposalInputs(stored, candidate) {
		return core.ProfileStateProposal{}, false, errors.New("profile state proposal conflicts with existing contents")
	}
	return stored, false, nil
}

func (store *Store) ProfileStateProposal(ctx context.Context, id core.ProfileStateProposalID) (core.ProfileStateProposal, error) {
	if id == "" {
		return core.ProfileStateProposal{}, errors.New("profile state proposal requires id")
	}
	return scanProfileStateProposal(store.db.QueryRowContext(ctx,
		`SELECT `+profileStateProposalColumns+` FROM profile_state_proposals WHERE id = ?`, id))
}

func (store *Store) profileStateProposalByIdempotencyKey(ctx context.Context, key string) (core.ProfileStateProposal, error) {
	return scanProfileStateProposal(store.db.QueryRowContext(ctx,
		`SELECT `+profileStateProposalColumns+` FROM profile_state_proposals WHERE idempotency_key = ?`, key))
}

func (store *Store) ListProfileStateProposals(ctx context.Context, filter storage.ProfileStateProposalFilter) ([]core.ProfileStateProposal, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT `+profileStateProposalColumns+`
		FROM profile_state_proposals
		WHERE (? = '' OR resource_tag = ?)
		  AND (? = '' OR profile_id = ?)
		  AND (? = '' OR status = ?)
		ORDER BY created_at DESC, id`,
		filter.ResourceTag, filter.ResourceTag, filter.ProfileID, filter.ProfileID, filter.Status, filter.Status)
	if err != nil {
		return nil, fmt.Errorf("list profile state proposals: %w", err)
	}
	defer rows.Close()
	proposals := make([]core.ProfileStateProposal, 0)
	for rows.Next() {
		proposal, err := scanProfileStateProposal(rows)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, proposal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate profile state proposals: %w", err)
	}
	return proposals, nil
}

func scanProfileStateProposal(row rowScanner) (core.ProfileStateProposal, error) {
	var proposal core.ProfileStateProposal
	var desiredState, changes []byte
	var observationTime, createdAt, updatedAt int64
	if err := row.Scan(
		&proposal.ID, &proposal.ResourceTag, &proposal.ProfileID, &proposal.Ownership, &proposal.Status,
		&proposal.IdempotencyKey, &proposal.ManifestDigest, &proposal.ObservedDigest, &proposal.DesiredDigest,
		&proposal.RemoteRevision, &desiredState, &changes, &observationTime, &proposal.Revision, &createdAt, &updatedAt,
	); err != nil {
		return core.ProfileStateProposal{}, err
	}
	proposal.DesiredState = append(json.RawMessage(nil), desiredState...)
	if err := json.Unmarshal(changes, &proposal.Changes); err != nil {
		return core.ProfileStateProposal{}, fmt.Errorf("decode profile state proposal changes: %w", err)
	}
	proposal.ObservationTime = time.Unix(0, observationTime).UTC()
	proposal.CreatedAt = time.Unix(0, createdAt).UTC()
	proposal.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if err := proposal.Validate(); err != nil {
		return core.ProfileStateProposal{}, fmt.Errorf("invalid stored profile state proposal %s: %w", proposal.ID, err)
	}
	return proposal, nil
}

func sameSQLiteProfileStateProposalInputs(left, right core.ProfileStateProposal) bool {
	return left.ResourceTag == right.ResourceTag && left.ProfileID == right.ProfileID && left.Ownership == right.Ownership &&
		left.Status == right.Status && left.IdempotencyKey == right.IdempotencyKey && left.ManifestDigest == right.ManifestDigest &&
		left.ObservedDigest == right.ObservedDigest && left.DesiredDigest == right.DesiredDigest && left.RemoteRevision == right.RemoteRevision &&
		bytes.Equal(left.DesiredState, right.DesiredState) && slices.Equal(left.Changes, right.Changes)
}

func cloneSQLiteProfileStateProposal(proposal core.ProfileStateProposal) core.ProfileStateProposal {
	proposal.DesiredState = append(json.RawMessage(nil), proposal.DesiredState...)
	proposal.Changes = slices.Clone(proposal.Changes)
	return proposal
}
