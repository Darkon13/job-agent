CREATE TABLE application_pacing_reservations (
    application_id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    scheduled_at INTEGER NOT NULL,
    interval_ns INTEGER NOT NULL CHECK (interval_ns > 0),
    acquired_at INTEGER,
    FOREIGN KEY (application_id) REFERENCES applications(id)
);

CREATE INDEX application_pacing_scope_idx
    ON application_pacing_reservations(profile_id, platform, scheduled_at);
