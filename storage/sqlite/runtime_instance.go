package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AcquireRuntimeInstance takes the exclusive runtime lease of this database.
// A lease older than ttl is considered abandoned and may be taken over, so a
// crashed process does not block the next start. A second live instance is
// refused instead of silently sharing a single-writer SQLite file.
func (store *Store) AcquireRuntimeInstance(ctx context.Context, owner string, now time.Time, ttl time.Duration) (bool, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || ttl <= 0 || now.IsZero() {
		return false, errors.New("runtime instance lease requires owner, time and positive ttl")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin runtime instance lease: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO runtime_instances
		(id, owner, acquired_at, renewed_at) VALUES (1, ?, ?, ?)`,
		owner, now.UnixNano(), now.UnixNano())
	if err != nil {
		return false, fmt.Errorf("insert runtime instance lease: %w", err)
	}
	inserted, err := oneRowAffected(result)
	if err != nil {
		return false, err
	}
	if !inserted {
		result, err = tx.ExecContext(ctx, `UPDATE runtime_instances SET owner = ?, acquired_at = ?, renewed_at = ?
			WHERE id = 1 AND renewed_at < ?`,
			owner, now.UnixNano(), now.UnixNano(), now.Add(-ttl).UnixNano())
		if err != nil {
			return false, fmt.Errorf("take over runtime instance lease: %w", err)
		}
		inserted, err = oneRowAffected(result)
		if err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit runtime instance lease: %w", err)
	}
	return inserted, nil
}

// RenewRuntimeInstance extends the lease owned by this instance. It returns
// false when another instance took the lease over.
func (store *Store) RenewRuntimeInstance(ctx context.Context, owner string, now time.Time) (bool, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || now.IsZero() {
		return false, errors.New("runtime instance renewal requires owner and time")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE runtime_instances SET renewed_at = ? WHERE id = 1 AND owner = ?`, now.UnixNano(), owner)
	if err != nil {
		return false, fmt.Errorf("renew runtime instance lease: %w", err)
	}
	renewed, err := oneRowAffected(result)
	if err != nil {
		return false, err
	}
	return renewed, nil
}

// ReleaseRuntimeInstance drops the lease when this instance still owns it.
func (store *Store) ReleaseRuntimeInstance(ctx context.Context, owner string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return errors.New("runtime instance release requires owner")
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM runtime_instances WHERE id = 1 AND owner = ?`, owner); err != nil {
		return fmt.Errorf("release runtime instance lease: %w", err)
	}
	return nil
}
