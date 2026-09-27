package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/scheduler"
	"github.com/Darkon13/job-agent/storage"
)

var _ scheduler.PauseStore = (*Store)(nil)

// PauseJob pauses one job for one profile. Repeating the call refreshes the
// reason instead of failing.
func (store *Store) PauseJob(ctx context.Context, jobTag string, profileID core.ProfileID, reason string, now time.Time) error {
	if jobTag == "" || profileID == "" {
		return fmt.Errorf("job pause requires job tag and profile")
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO job_pauses (job_tag, profile_id, reason, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(job_tag, profile_id) DO UPDATE SET reason = excluded.reason, created_at = excluded.created_at`,
		jobTag, profileID, reason, now.UnixNano()); err != nil {
		return fmt.Errorf("pause job %s for %s: %w", jobTag, profileID, err)
	}
	return nil
}

// PauseJobs pauses a job for every profile that currently has a schedule for
// it and reports how many rows were affected.
func (store *Store) PauseJobs(ctx context.Context, jobTag string, reason string, now time.Time) (int, error) {
	if jobTag == "" {
		return 0, fmt.Errorf("job pause requires a job tag")
	}
	result, err := store.db.ExecContext(ctx, `INSERT INTO job_pauses (job_tag, profile_id, reason, created_at)
		SELECT DISTINCT job_tag, profile_id, ?, ? FROM scheduled_jobs WHERE job_tag = ? AND profile_id <> ''
		ON CONFLICT(job_tag, profile_id) DO UPDATE SET reason = excluded.reason, created_at = excluded.created_at`,
		reason, now.UnixNano(), jobTag)
	if err != nil {
		return 0, fmt.Errorf("pause job %s: %w", jobTag, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count paused rows for %s: %w", jobTag, err)
	}
	return int(affected), nil
}

// ResumeJob removes pauses for one job: a specific profile or every profile
// when the profile is empty.
func (store *Store) ResumeJob(ctx context.Context, jobTag string, profileID core.ProfileID) (int, error) {
	if jobTag == "" {
		return 0, fmt.Errorf("job resume requires a job tag")
	}
	query := `DELETE FROM job_pauses WHERE job_tag = ?`
	args := []any{jobTag}
	if profileID != "" {
		query += ` AND profile_id = ?`
		args = append(args, profileID)
	}
	result, err := store.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("resume job %s: %w", jobTag, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count resumed rows for %s: %w", jobTag, err)
	}
	return int(affected), nil
}

// JobPauses lists every paused (job, profile) pair.
func (store *Store) JobPauses(ctx context.Context) ([]scheduler.Pause, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT job_tag, profile_id, reason, created_at
		FROM job_pauses ORDER BY job_tag, profile_id`)
	if err != nil {
		return nil, fmt.Errorf("load job pauses: %w", err)
	}
	defer rows.Close()
	pauses := make([]scheduler.Pause, 0)
	for rows.Next() {
		var (
			pause     scheduler.Pause
			createdAt int64
		)
		if err := rows.Scan(&pause.JobTag, &pause.ProfileID, &pause.Reason, &createdAt); err != nil {
			return nil, fmt.Errorf("scan job pause: %w", err)
		}
		pause.CreatedAt = time.Unix(0, createdAt).UTC()
		pauses = append(pauses, pause)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job pauses: %w", err)
	}
	return pauses, nil
}

// ListJobPauses exposes the pause records for the API.
func (store *Store) ListJobPauses(ctx context.Context) ([]storage.JobPause, error) {
	pauses, err := store.JobPauses(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]storage.JobPause, 0, len(pauses))
	for _, pause := range pauses {
		items = append(items, storage.JobPause{
			JobTag: pause.JobTag, ProfileID: pause.ProfileID, Reason: pause.Reason, CreatedAt: pause.CreatedAt,
		})
	}
	return items, nil
}
