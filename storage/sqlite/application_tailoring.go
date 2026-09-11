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

const applicationTailoringColumns = `id, application_id, attempt, profile_id, platform, external_id, resume_id,
	status, idempotency_key, processor_tag, processor_version, processor_input_digest,
	allowed_paths, baseline_digest, tailored_digest, baseline_remote_revision,
	baseline_observed_at, tailored_remote_revision, baseline_state, tailored_state, apply_proposal_id,
	restore_proposal_id, recovery_reason, revision, created_at, updated_at`

func (store *Store) CreateApplicationTailoring(ctx context.Context, candidate core.ApplicationTailoring) (core.ApplicationTailoring, bool, error) {
	if err := candidate.Validate(); err != nil {
		return core.ApplicationTailoring{}, false, err
	}
	paths, err := json.Marshal(candidate.AllowedPaths)
	if err != nil {
		return core.ApplicationTailoring{}, false, fmt.Errorf("encode application tailoring paths: %w", err)
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO application_tailorings (`+applicationTailoringColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidate.ID, candidate.ApplicationID, candidate.Attempt, candidate.Key.ProfileID, candidate.Key.Vacancy.Platform,
		candidate.Key.Vacancy.ExternalID, candidate.ResumeID, candidate.Status, candidate.IdempotencyKey,
		candidate.ProcessorTag, candidate.ProcessorVersion, candidate.ProcessorInputDigest, paths,
		candidate.BaselineDigest, candidate.TailoredDigest, candidate.BaselineRemoteRevision,
		candidate.BaselineObservedAt.UnixNano(), candidate.TailoredRemoteRevision,
		[]byte(candidate.BaselineState), []byte(candidate.TailoredState),
		candidate.ApplyProposalID, candidate.RestoreProposalID, candidate.RecoveryReason,
		candidate.Revision, candidate.CreatedAt.UnixNano(), candidate.UpdatedAt.UnixNano())
	if err != nil {
		return core.ApplicationTailoring{}, false, fmt.Errorf("insert application tailoring %s: %w", candidate.ID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil {
		return core.ApplicationTailoring{}, false, err
	}
	if created {
		return cloneSQLiteApplicationTailoring(candidate), true, nil
	}
	stored, loadErr := store.ApplicationTailoringByApplication(ctx, candidate.ApplicationID)
	if loadErr == nil {
		if !sameSQLiteApplicationTailoringInputs(stored, candidate) {
			return core.ApplicationTailoring{}, false, errors.New("application already has a different tailoring workflow")
		}
		return stored, false, nil
	}
	active, activeErr := store.activeApplicationTailoring(ctx, candidate.Key.ProfileID)
	if activeErr == nil {
		return core.ApplicationTailoring{}, false, errors.Join(storage.ErrProfileMutationLocked, errors.New(string(active.ID)))
	}
	stored, loadErr = store.ApplicationTailoring(ctx, candidate.ID)
	if loadErr == nil {
		if !sameSQLiteApplicationTailoringInputs(stored, candidate) {
			return core.ApplicationTailoring{}, false, errors.New("application tailoring id conflicts with different contents")
		}
		return stored, false, nil
	}
	return core.ApplicationTailoring{}, false, errors.New("application tailoring insert conflicted with an existing workflow")
}

func (store *Store) ApplicationTailoring(ctx context.Context, id core.ApplicationTailoringID) (core.ApplicationTailoring, error) {
	if id == "" {
		return core.ApplicationTailoring{}, errors.New("application tailoring requires id")
	}
	tailoring, err := scanApplicationTailoring(store.db.QueryRowContext(ctx,
		`SELECT `+applicationTailoringColumns+` FROM application_tailorings WHERE id = ?`, id))
	return tailoring, applicationTailoringLookupError(err)
}

func (store *Store) ApplicationTailoringByApplication(ctx context.Context, applicationID core.ApplicationID) (core.ApplicationTailoring, error) {
	if applicationID == "" {
		return core.ApplicationTailoring{}, errors.New("application tailoring requires application id")
	}
	tailoring, err := scanApplicationTailoring(store.db.QueryRowContext(ctx,
		`SELECT `+applicationTailoringColumns+` FROM application_tailorings
		 WHERE application_id = ? AND status <> 'restored' ORDER BY attempt DESC LIMIT 1`, applicationID))
	return tailoring, applicationTailoringLookupError(err)
}

func (store *Store) activeApplicationTailoring(ctx context.Context, profileID core.ProfileID) (core.ApplicationTailoring, error) {
	tailoring, err := scanApplicationTailoring(store.db.QueryRowContext(ctx,
		`SELECT `+applicationTailoringColumns+` FROM application_tailorings WHERE profile_id = ? AND status <> 'restored' LIMIT 1`, profileID))
	return tailoring, applicationTailoringLookupError(err)
}

func applicationTailoringLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrApplicationTailoringNotFound
	}
	return err
}

func (store *Store) SaveApplicationTailoring(ctx context.Context, candidate core.ApplicationTailoring, expectedRevision uint64) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Revision != expectedRevision+1 {
		return storage.ErrRevisionConflict
	}
	stored, err := store.ApplicationTailoring(ctx, candidate.ID)
	if err != nil {
		return err
	}
	if !sameSQLiteApplicationTailoringInputs(stored, candidate) {
		return errors.New("application tailoring immutable inputs changed")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE application_tailorings SET
		status = ?, tailored_remote_revision = ?, apply_proposal_id = ?, restore_proposal_id = ?,
		recovery_reason = ?, revision = ?, updated_at = ? WHERE id = ? AND revision = ?`,
		candidate.Status, candidate.TailoredRemoteRevision, candidate.ApplyProposalID,
		candidate.RestoreProposalID, candidate.RecoveryReason, candidate.Revision,
		candidate.UpdatedAt.UnixNano(), candidate.ID, expectedRevision)
	if err != nil {
		return fmt.Errorf("save application tailoring %s: %w", candidate.ID, err)
	}
	updated, err := oneRowAffected(result)
	if err != nil {
		return err
	}
	if !updated {
		return storage.ErrRevisionConflict
	}
	return nil
}

func scanApplicationTailoring(row rowScanner) (core.ApplicationTailoring, error) {
	var tailoring core.ApplicationTailoring
	var allowedPaths, baselineState, tailoredState []byte
	var baselineObservedAt, createdAt, updatedAt int64
	if err := row.Scan(
		&tailoring.ID, &tailoring.ApplicationID, &tailoring.Attempt, &tailoring.Key.ProfileID, &tailoring.Key.Vacancy.Platform,
		&tailoring.Key.Vacancy.ExternalID, &tailoring.ResumeID, &tailoring.Status, &tailoring.IdempotencyKey,
		&tailoring.ProcessorTag, &tailoring.ProcessorVersion, &tailoring.ProcessorInputDigest,
		&allowedPaths, &tailoring.BaselineDigest, &tailoring.TailoredDigest,
		&tailoring.BaselineRemoteRevision, &baselineObservedAt, &tailoring.TailoredRemoteRevision,
		&baselineState, &tailoredState, &tailoring.ApplyProposalID, &tailoring.RestoreProposalID,
		&tailoring.RecoveryReason, &tailoring.Revision, &createdAt, &updatedAt,
	); err != nil {
		return core.ApplicationTailoring{}, err
	}
	if err := json.Unmarshal(allowedPaths, &tailoring.AllowedPaths); err != nil {
		return core.ApplicationTailoring{}, fmt.Errorf("decode application tailoring paths: %w", err)
	}
	tailoring.BaselineState = append(json.RawMessage(nil), baselineState...)
	tailoring.TailoredState = append(json.RawMessage(nil), tailoredState...)
	tailoring.BaselineObservedAt = time.Unix(0, baselineObservedAt).UTC()
	tailoring.CreatedAt = time.Unix(0, createdAt).UTC()
	tailoring.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if err := tailoring.Validate(); err != nil {
		return core.ApplicationTailoring{}, fmt.Errorf("invalid stored application tailoring %s: %w", tailoring.ID, err)
	}
	return tailoring, nil
}

func sameSQLiteApplicationTailoringInputs(left, right core.ApplicationTailoring) bool {
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

func cloneSQLiteApplicationTailoring(tailoring core.ApplicationTailoring) core.ApplicationTailoring {
	tailoring.AllowedPaths = slices.Clone(tailoring.AllowedPaths)
	tailoring.BaselineState = append(json.RawMessage(nil), tailoring.BaselineState...)
	tailoring.TailoredState = append(json.RawMessage(nil), tailoring.TailoredState...)
	return tailoring
}
