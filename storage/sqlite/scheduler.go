package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/scheduler"
)

var _ scheduler.Store = (*Store)(nil)

func (store *Store) SyncSchedules(ctx context.Context, entries []scheduler.Entry, now time.Time) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schedule sync: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE scheduled_jobs SET enabled = 0, updated_at = ?`, now.UnixNano()); err != nil {
		return fmt.Errorf("disable old schedules: %w", err)
	}
	for _, entry := range entries {
		if err := entry.Definition.Validate(); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO scheduled_jobs
			(job_tag, trigger_index, expression, timezone, action_type, platform, profile_id, payload,
			 priority, jitter_min_ns, jitter_max_ns, next_run_at, enabled, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
			ON CONFLICT(job_tag, trigger_index) DO UPDATE SET
			 next_run_at = CASE WHEN
				scheduled_jobs.expression != excluded.expression OR scheduled_jobs.timezone != excluded.timezone OR
				scheduled_jobs.action_type != excluded.action_type OR scheduled_jobs.platform != excluded.platform OR
				scheduled_jobs.profile_id != excluded.profile_id OR scheduled_jobs.payload != excluded.payload OR
				scheduled_jobs.priority != excluded.priority OR
				scheduled_jobs.jitter_min_ns != excluded.jitter_min_ns OR scheduled_jobs.jitter_max_ns != excluded.jitter_max_ns
			 THEN excluded.next_run_at ELSE scheduled_jobs.next_run_at END,
			 expression = excluded.expression, timezone = excluded.timezone, action_type = excluded.action_type,
			 platform = excluded.platform, profile_id = excluded.profile_id, payload = excluded.payload,
			 priority = excluded.priority,
			 jitter_min_ns = excluded.jitter_min_ns, jitter_max_ns = excluded.jitter_max_ns,
			 enabled = 1, updated_at = excluded.updated_at`,
			entry.JobTag, entry.TriggerIndex, entry.Expression, entry.Timezone, entry.ActionType,
			entry.Platform, entry.ProfileID, []byte(entry.Payload), entry.Priority,
			entry.JitterMin.Nanoseconds(), entry.JitterMax.Nanoseconds(),
			entry.NextRunAt.UnixNano(), now.UnixNano())
		if err != nil {
			return fmt.Errorf("sync schedule %s/%d: %w", entry.JobTag, entry.TriggerIndex, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schedule sync: %w", err)
	}
	return nil
}

func (store *Store) DueSchedules(ctx context.Context, now time.Time, limit int) ([]scheduler.Entry, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("due schedule limit must be positive")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT job_tag, trigger_index, expression, timezone,
		action_type, platform, profile_id, payload, priority, jitter_min_ns, jitter_max_ns, next_run_at
		FROM scheduled_jobs WHERE enabled = 1 AND next_run_at <= ? ORDER BY next_run_at, job_tag, trigger_index LIMIT ?`,
		now.UnixNano(), limit)
	if err != nil {
		return nil, fmt.Errorf("query due schedules: %w", err)
	}
	defer rows.Close()
	return scanScheduledEntries(rows)
}

// Schedules lists every enabled schedule with its next run, regardless of
// whether the run is due. The dashboard joins it with the runnable jobs from
// the configuration to show the countdown before enqueue.
func (store *Store) Schedules(ctx context.Context) ([]scheduler.Entry, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT job_tag, trigger_index, expression, timezone,
		action_type, platform, profile_id, payload, priority, jitter_min_ns, jitter_max_ns, next_run_at
		FROM scheduled_jobs WHERE enabled = 1 ORDER BY job_tag, trigger_index`)
	if err != nil {
		return nil, fmt.Errorf("query schedules: %w", err)
	}
	defer rows.Close()
	return scanScheduledEntries(rows)
}

func scanScheduledEntries(rows *sql.Rows) ([]scheduler.Entry, error) {
	entries := make([]scheduler.Entry, 0)
	for rows.Next() {
		var entry scheduler.Entry
		var payload []byte
		var jitterMin, jitterMax, nextRunAt int64
		if err := rows.Scan(&entry.JobTag, &entry.TriggerIndex, &entry.Expression, &entry.Timezone,
			&entry.ActionType, &entry.Platform, &entry.ProfileID, &payload, &entry.Priority,
			&jitterMin, &jitterMax, &nextRunAt); err != nil {
			return nil, fmt.Errorf("scan scheduled entry: %w", err)
		}
		entry.Payload = bytes.Clone(payload)
		entry.JitterMin = time.Duration(jitterMin)
		entry.JitterMax = time.Duration(jitterMax)
		entry.NextRunAt = time.Unix(0, nextRunAt).UTC()
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (store *Store) HasActiveScheduledTask(ctx context.Context, jobTag string) (bool, error) {
	var active bool
	err := store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks
		WHERE source = ? AND status IN (?, ?, ?, ?))`, "cron:"+jobTag,
		core.TaskNew, core.TaskProcessing, core.TaskWaitingConfirmation, core.TaskRetryScheduled).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("check active scheduled task %s: %w", jobTag, err)
	}
	return active, nil
}

func (store *Store) AdvanceSchedule(ctx context.Context, jobTag string, triggerIndex int, expected, next, now time.Time) (bool, error) {
	result, err := store.db.ExecContext(ctx, `UPDATE scheduled_jobs SET next_run_at = ?, updated_at = ?
		WHERE job_tag = ? AND trigger_index = ? AND enabled = 1 AND next_run_at = ?`,
		next.UnixNano(), now.UnixNano(), jobTag, triggerIndex, expected.UnixNano())
	if err != nil {
		return false, fmt.Errorf("advance schedule %s/%d: %w", jobTag, triggerIndex, err)
	}
	return oneRowAffected(result)
}
