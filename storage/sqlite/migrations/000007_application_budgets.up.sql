CREATE TABLE application_budget_buckets (
    profile_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    window_start INTEGER NOT NULL,
    window_end INTEGER NOT NULL,
    limit_value INTEGER NOT NULL CHECK (limit_value > 0),
    used INTEGER NOT NULL DEFAULT 0 CHECK (used >= 0 AND used <= limit_value),
    PRIMARY KEY (profile_id, platform, window_start)
);

CREATE TABLE application_budget_reservations (
    application_id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    window_start INTEGER NOT NULL,
    window_end INTEGER NOT NULL,
    limit_value INTEGER NOT NULL CHECK (limit_value > 0),
    state TEXT NOT NULL CHECK (state IN ('reserved', 'committed', 'released')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (application_id) REFERENCES applications(id),
    FOREIGN KEY (profile_id, platform, window_start)
        REFERENCES application_budget_buckets(profile_id, platform, window_start)
);

CREATE INDEX application_budget_state_idx
    ON application_budget_reservations(profile_id, platform, window_start, state);
