CREATE TABLE application_platform_states (
    application_id TEXT NOT NULL PRIMARY KEY,
    external_negotiation_id TEXT NOT NULL,
    platform_state TEXT NOT NULL,
    disposition TEXT NOT NULL CHECK (disposition IN (
        'pending', 'invited', 'rejected', 'hidden', 'unknown'
    )),
    viewed_by_opponent INTEGER,
    platform_updated_at INTEGER,
    observed_at INTEGER NOT NULL,
    FOREIGN KEY (application_id) REFERENCES applications(id) ON DELETE CASCADE
);

CREATE INDEX application_platform_states_retention_idx
    ON application_platform_states(disposition, observed_at DESC);
