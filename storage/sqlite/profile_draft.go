package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var _ storage.ProfileDraftRepository = (*Store)(nil)

// CreateProfileDraft inserts a dashboard-managed profile draft unless the tag
// is already used. The existing row is returned with created=false.
func (store *Store) CreateProfileDraft(ctx context.Context, draft core.ProfileDraft) (core.ProfileDraft, bool, error) {
	if err := draft.Validate(); err != nil {
		return core.ProfileDraft{}, false, err
	}
	identity, resumes, err := encodeProfileDraftPayload(draft)
	if err != nil {
		return core.ProfileDraft{}, false, err
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO profile_drafts
		(tag, platform, adapter, state_file, identity, resumes, status, created_at, updated_at, revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		draft.Tag, draft.Platform, draft.Adapter, draft.StateFile, identity, resumes, draft.Status,
		draft.CreatedAt.UnixNano(), draft.UpdatedAt.UnixNano(), draft.Revision)
	if err != nil {
		return core.ProfileDraft{}, false, fmt.Errorf("create profile draft %s: %w", draft.Tag, err)
	}
	created, err := oneRowAffected(result)
	if err != nil {
		return core.ProfileDraft{}, false, err
	}
	if created {
		return draft, true, nil
	}
	stored, err := store.ProfileDraft(ctx, draft.Tag)
	if err != nil {
		return core.ProfileDraft{}, false, err
	}
	return stored, false, nil
}

// SaveProfileDraft replaces a draft with compare-and-swap on the revision.
func (store *Store) SaveProfileDraft(ctx context.Context, draft core.ProfileDraft, expectedRevision uint64) error {
	if err := draft.Validate(); err != nil {
		return err
	}
	identity, resumes, err := encodeProfileDraftPayload(draft)
	if err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `UPDATE profile_drafts SET
		platform = ?, adapter = ?, state_file = ?, identity = ?, resumes = ?, status = ?,
		created_at = ?, updated_at = ?, revision = ?
		WHERE tag = ? AND revision = ?`,
		draft.Platform, draft.Adapter, draft.StateFile, identity, resumes, draft.Status,
		draft.CreatedAt.UnixNano(), draft.UpdatedAt.UnixNano(), draft.Revision, draft.Tag, expectedRevision)
	if err != nil {
		return fmt.Errorf("save profile draft %s: %w", draft.Tag, err)
	}
	updated, err := oneRowAffected(result)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("save profile draft %s: %w", draft.Tag, storage.ErrRevisionConflict)
	}
	return nil
}

func (store *Store) ProfileDraft(ctx context.Context, tag string) (core.ProfileDraft, error) {
	if tag == "" {
		return core.ProfileDraft{}, errors.New("profile draft tag is required")
	}
	row := store.db.QueryRowContext(ctx, `SELECT tag, platform, adapter, state_file, identity, resumes,
		status, created_at, updated_at, revision FROM profile_drafts WHERE tag = ?`, tag)
	draft, err := scanProfileDraft(row)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ProfileDraft{}, storage.ErrProfileDraftNotFound
	}
	return draft, err
}

func (store *Store) ProfileDrafts(ctx context.Context) ([]core.ProfileDraft, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT tag, platform, adapter, state_file, identity, resumes,
		status, created_at, updated_at, revision FROM profile_drafts ORDER BY created_at, tag`)
	if err != nil {
		return nil, fmt.Errorf("load profile drafts: %w", err)
	}
	defer rows.Close()
	drafts := make([]core.ProfileDraft, 0)
	for rows.Next() {
		draft, err := scanProfileDraft(rows)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, draft)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate profile drafts: %w", err)
	}
	return drafts, nil
}

func (store *Store) DeleteProfileDraft(ctx context.Context, tag string) error {
	if tag == "" {
		return errors.New("profile draft tag is required")
	}
	result, err := store.db.ExecContext(ctx, `DELETE FROM profile_drafts WHERE tag = ?`, tag)
	if err != nil {
		return fmt.Errorf("delete profile draft %s: %w", tag, err)
	}
	deleted, err := oneRowAffected(result)
	if err != nil {
		return err
	}
	if !deleted {
		return storage.ErrProfileDraftNotFound
	}
	return nil
}

type profileDraftScanner interface {
	Scan(dest ...any) error
}

func scanProfileDraft(row profileDraftScanner) (core.ProfileDraft, error) {
	var (
		draft              core.ProfileDraft
		identity           []byte
		resumes            []byte
		createdAt, updated int64
	)
	if err := row.Scan(&draft.Tag, &draft.Platform, &draft.Adapter, &draft.StateFile, &identity, &resumes,
		&draft.Status, &createdAt, &updated, &draft.Revision); err != nil {
		return core.ProfileDraft{}, err
	}
	draft.CreatedAt = time.Unix(0, createdAt).UTC()
	draft.UpdatedAt = time.Unix(0, updated).UTC()
	if len(identity) != 0 {
		var decoded core.ProfileIdentity
		if err := json.Unmarshal(identity, &decoded); err != nil {
			return core.ProfileDraft{}, fmt.Errorf("decode profile draft identity: %w", err)
		}
		draft.Identity = &decoded
	}
	if len(resumes) != 0 {
		if err := json.Unmarshal(resumes, &draft.Resumes); err != nil {
			return core.ProfileDraft{}, fmt.Errorf("decode profile draft resumes: %w", err)
		}
	}
	if draft.Resumes == nil {
		draft.Resumes = []core.ProfileDraftResume{}
	}
	return draft, nil
}

func encodeProfileDraftPayload(draft core.ProfileDraft) ([]byte, []byte, error) {
	var identity []byte
	if draft.Identity != nil {
		encoded, err := json.Marshal(draft.Identity)
		if err != nil {
			return nil, nil, fmt.Errorf("encode profile draft identity: %w", err)
		}
		identity = encoded
	}
	resumes, err := json.Marshal(draft.Resumes)
	if err != nil {
		return nil, nil, fmt.Errorf("encode profile draft resumes: %w", err)
	}
	return identity, resumes, nil
}
