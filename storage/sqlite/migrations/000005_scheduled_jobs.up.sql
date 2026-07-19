CREATE TABLE scheduled_jobs (
    job_tag TEXT NOT NULL,
    trigger_index INTEGER NOT NULL,
    expression TEXT NOT NULL,
    timezone TEXT NOT NULL,
    action_type TEXT NOT NULL,
    platform TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    payload BLOB NOT NULL,
    jitter_min_ns INTEGER NOT NULL,
    jitter_max_ns INTEGER NOT NULL,
    next_run_at INTEGER NOT NULL,
    enabled INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (job_tag, trigger_index)
);

CREATE INDEX scheduled_jobs_due_idx ON scheduled_jobs(enabled, next_run_at);
