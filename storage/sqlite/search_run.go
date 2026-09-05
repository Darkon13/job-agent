package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func (store *Store) CreateSearchRun(ctx context.Context, candidate core.SearchRun) (core.SearchRun, bool, error) {
	if err := candidate.Validate(); err != nil {
		return core.SearchRun{}, false, err
	}
	profiles, err := json.Marshal(candidate.TargetProfiles)
	if err != nil {
		return core.SearchRun{}, false, fmt.Errorf("encode search profiles: %w", err)
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO search_runs
		(search_id, adapter, platform, search_profile_id, target_profiles, query, correlation_id, cursor, done, revision, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidate.SearchID, candidate.Adapter, candidate.Platform, candidate.SearchProfileID, profiles, []byte(candidate.Query),
		candidate.CorrelationID, candidate.Cursor, candidate.Done, candidate.Revision, candidate.CreatedAt.UnixNano(), candidate.UpdatedAt.UnixNano())
	if err != nil {
		return core.SearchRun{}, false, fmt.Errorf("insert search run %s: %w", candidate.SearchID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil {
		return core.SearchRun{}, false, err
	}
	stored, err := store.SearchRun(ctx, candidate.SearchID)
	if err != nil {
		return core.SearchRun{}, false, err
	}
	if !sameSearchDefinition(stored, candidate) {
		return core.SearchRun{}, false, fmt.Errorf("search run %s conflicts with changed configuration", candidate.SearchID)
	}
	return stored, created, nil
}

func (store *Store) SearchRun(ctx context.Context, searchID core.SearchID) (core.SearchRun, error) {
	if searchID == "" {
		return core.SearchRun{}, errors.New("search run requires search id")
	}
	row := store.db.QueryRowContext(ctx, `SELECT adapter, platform, search_profile_id, target_profiles, query, correlation_id,
		cursor, done, revision, created_at, updated_at FROM search_runs WHERE search_id = ?`, searchID)
	var run core.SearchRun
	var profiles, query []byte
	var createdAt, updatedAt int64
	run.SearchID = searchID
	if err := row.Scan(&run.Adapter, &run.Platform, &run.SearchProfileID, &profiles, &query, &run.CorrelationID,
		&run.Cursor, &run.Done, &run.Revision, &createdAt, &updatedAt); err != nil {
		return core.SearchRun{}, err
	}
	if err := json.Unmarshal(profiles, &run.TargetProfiles); err != nil {
		return core.SearchRun{}, fmt.Errorf("decode search run profiles: %w", err)
	}
	run.Query = append(run.Query[:0], query...)
	run.CreatedAt = time.Unix(0, createdAt).UTC()
	run.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if err := run.Validate(); err != nil {
		return core.SearchRun{}, fmt.Errorf("invalid stored search run %s: %w", searchID, err)
	}
	return run, nil
}

func (store *Store) SaveSearchRun(ctx context.Context, candidate core.SearchRun, expectedRevision uint64) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Revision != expectedRevision+1 {
		return errors.New("search run revision must advance by one")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE search_runs SET cursor = ?, done = ?, revision = ?, updated_at = ?
		WHERE search_id = ? AND revision = ?`, candidate.Cursor, candidate.Done, candidate.Revision,
		candidate.UpdatedAt.UnixNano(), candidate.SearchID, expectedRevision)
	if err != nil {
		return fmt.Errorf("save search run %s: %w", candidate.SearchID, err)
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

func sameSearchDefinition(left, right core.SearchRun) bool {
	return left.SearchID == right.SearchID && left.Adapter == right.Adapter && left.Platform == right.Platform &&
		left.SearchProfileID == right.SearchProfileID && slices.Equal(left.TargetProfiles, right.TargetProfiles) &&
		bytes.Equal(left.Query, right.Query)
}
