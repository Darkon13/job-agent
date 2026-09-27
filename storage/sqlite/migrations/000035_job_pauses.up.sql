CREATE TABLE job_pauses (
    job_tag TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (job_tag, profile_id)
);
