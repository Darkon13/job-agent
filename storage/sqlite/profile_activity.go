package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

const profileActivityColumns = `id, platform, profile_id, resume_id, kind, source_id, occurred_at`
const profileActivitySnapshotColumns = `id, platform, profile_id, resume_id, source_id,
	score, score_hidden, period_days, search_shows, views, new_views, invitations, new_invitations,
	response_streak, responses_required, observed_at`

func (store *Store) RecordProfileActivity(ctx context.Context, candidate core.ProfileActivityRecord) (bool, error) {
	if err := candidate.Validate(); err != nil {
		return false, err
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO profile_activity (`+profileActivityColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		candidate.ID, candidate.Platform, candidate.ProfileID, candidate.ResumeID, candidate.Kind,
		candidate.SourceID, candidate.OccurredAt.UnixNano())
	if err != nil {
		return false, fmt.Errorf("insert profile activity %s: %w", candidate.ID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil || created {
		return created, err
	}
	stored, err := scanProfileActivity(store.db.QueryRowContext(ctx,
		`SELECT `+profileActivityColumns+` FROM profile_activity WHERE id = ?`, candidate.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, errors.New("profile activity identity conflicts with another stored record")
	}
	if err != nil {
		return false, fmt.Errorf("load existing profile activity: %w", err)
	}
	if stored.Platform != candidate.Platform || stored.ProfileID != candidate.ProfileID || stored.ResumeID != candidate.ResumeID ||
		stored.Kind != candidate.Kind || stored.SourceID != candidate.SourceID {
		return false, errors.New("profile activity id conflicts with different identity")
	}
	return false, nil
}

func (store *Store) ListProfileActivity(ctx context.Context, filter storage.ProfileActivityFilter) ([]core.ProfileActivityRecord, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT `+profileActivityColumns+`
		FROM profile_activity
		WHERE (? = '' OR platform = ?)
		  AND (? = '' OR profile_id = ?)
		  AND (? = '' OR kind = ?)
		ORDER BY occurred_at DESC, id`,
		filter.Platform, filter.Platform, filter.ProfileID, filter.ProfileID, filter.Kind, filter.Kind)
	if err != nil {
		return nil, fmt.Errorf("list profile activity: %w", err)
	}
	defer rows.Close()
	records := make([]core.ProfileActivityRecord, 0)
	for rows.Next() {
		record, err := scanProfileActivity(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate profile activity: %w", err)
	}
	return records, nil
}

func (store *Store) ProfileActivityCounts(ctx context.Context, filter storage.ProfileActivityFilter) ([]storage.ProfileActivityCount, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT platform, profile_id, kind, COUNT(*), MAX(occurred_at)
		FROM profile_activity
		WHERE (? = '' OR platform = ?)
		  AND (? = '' OR profile_id = ?)
		  AND (? = '' OR kind = ?)
		GROUP BY platform, profile_id, kind
		ORDER BY profile_id, platform, kind`,
		filter.Platform, filter.Platform, filter.ProfileID, filter.ProfileID, filter.Kind, filter.Kind)
	if err != nil {
		return nil, fmt.Errorf("count profile activity: %w", err)
	}
	defer rows.Close()
	counts := make([]storage.ProfileActivityCount, 0)
	for rows.Next() {
		var item storage.ProfileActivityCount
		var occurredAt int64
		if err := rows.Scan(&item.Platform, &item.ProfileID, &item.Kind, &item.Count, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan profile activity count: %w", err)
		}
		item.LastOccurredAt = time.Unix(0, occurredAt).UTC()
		counts = append(counts, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate profile activity counts: %w", err)
	}
	return counts, nil
}

func scanProfileActivity(row rowScanner) (core.ProfileActivityRecord, error) {
	var record core.ProfileActivityRecord
	var occurredAt int64
	if err := row.Scan(&record.ID, &record.Platform, &record.ProfileID, &record.ResumeID, &record.Kind, &record.SourceID, &occurredAt); err != nil {
		return core.ProfileActivityRecord{}, err
	}
	record.OccurredAt = time.Unix(0, occurredAt).UTC()
	if err := record.Validate(); err != nil {
		return core.ProfileActivityRecord{}, fmt.Errorf("invalid stored profile activity %s: %w", record.ID, err)
	}
	return record, nil
}

func (store *Store) RecordProfileActivitySnapshot(ctx context.Context, candidate core.ProfileActivitySnapshot) (bool, error) {
	if err := candidate.Validate(); err != nil {
		return false, err
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO profile_activity_snapshots (`+profileActivitySnapshotColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidate.ID, candidate.Platform, candidate.ProfileID, candidate.ResumeID, candidate.SourceID,
		nullableInt(candidate.Score), candidate.ScoreHidden, nullableInt(candidate.PeriodDays), nullableInt(candidate.SearchShows),
		nullableInt(candidate.Views), nullableInt(candidate.NewViews), nullableInt(candidate.Invitations), nullableInt(candidate.NewInvitations),
		nullableInt(candidate.ResponseStreak), nullableInt(candidate.ResponsesRequired), candidate.ObservedAt.UnixNano())
	if err != nil {
		return false, fmt.Errorf("insert profile activity snapshot %s: %w", candidate.ID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil || created {
		return created, err
	}
	stored, err := scanProfileActivitySnapshot(store.db.QueryRowContext(ctx,
		`SELECT `+profileActivitySnapshotColumns+` FROM profile_activity_snapshots WHERE id = ?`, candidate.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, errors.New("profile activity snapshot identity conflicts with another stored snapshot")
	}
	if err != nil {
		return false, fmt.Errorf("load existing profile activity snapshot: %w", err)
	}
	if stored.Platform != candidate.Platform || stored.ProfileID != candidate.ProfileID || stored.ResumeID != candidate.ResumeID || stored.SourceID != candidate.SourceID {
		return false, errors.New("profile activity snapshot id conflicts with different identity")
	}
	return false, nil
}

func (store *Store) ListProfileActivitySnapshots(ctx context.Context, filter storage.ProfileActivitySnapshotFilter) ([]core.ProfileActivitySnapshot, error) {
	if filter.Limit < 0 {
		return nil, errors.New("profile activity snapshot limit cannot be negative")
	}
	limit := filter.Limit
	if limit == 0 {
		limit = -1
	}
	rows, err := store.db.QueryContext(ctx, `SELECT `+profileActivitySnapshotColumns+`
		FROM profile_activity_snapshots
		WHERE (? = '' OR platform = ?)
		  AND (? = '' OR profile_id = ?)
		  AND (? = '' OR resume_id = ?)
		ORDER BY observed_at DESC, id
		LIMIT ?`,
		filter.Platform, filter.Platform, filter.ProfileID, filter.ProfileID, filter.ResumeID, filter.ResumeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list profile activity snapshots: %w", err)
	}
	defer rows.Close()
	snapshots := make([]core.ProfileActivitySnapshot, 0)
	for rows.Next() {
		snapshot, err := scanProfileActivitySnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate profile activity snapshots: %w", err)
	}
	return snapshots, nil
}

func scanProfileActivitySnapshot(row rowScanner) (core.ProfileActivitySnapshot, error) {
	var snapshot core.ProfileActivitySnapshot
	var score, periodDays, searchShows, views, newViews sql.NullInt64
	var invitations, newInvitations, responseStreak, responsesRequired sql.NullInt64
	var observedAt int64
	if err := row.Scan(
		&snapshot.ID, &snapshot.Platform, &snapshot.ProfileID, &snapshot.ResumeID, &snapshot.SourceID,
		&score, &snapshot.ScoreHidden, &periodDays, &searchShows, &views, &newViews, &invitations, &newInvitations,
		&responseStreak, &responsesRequired, &observedAt,
	); err != nil {
		return core.ProfileActivitySnapshot{}, err
	}
	snapshot.Score = intFromNull(score)
	snapshot.PeriodDays = intFromNull(periodDays)
	snapshot.SearchShows = intFromNull(searchShows)
	snapshot.Views = intFromNull(views)
	snapshot.NewViews = intFromNull(newViews)
	snapshot.Invitations = intFromNull(invitations)
	snapshot.NewInvitations = intFromNull(newInvitations)
	snapshot.ResponseStreak = intFromNull(responseStreak)
	snapshot.ResponsesRequired = intFromNull(responsesRequired)
	snapshot.ObservedAt = time.Unix(0, observedAt).UTC()
	if err := snapshot.Validate(); err != nil {
		return core.ProfileActivitySnapshot{}, fmt.Errorf("invalid stored profile activity snapshot %s: %w", snapshot.ID, err)
	}
	return snapshot, nil
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func intFromNull(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
}
