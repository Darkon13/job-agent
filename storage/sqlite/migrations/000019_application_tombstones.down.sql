-- A rollback cannot reconstruct deleted application content. Restore a backup
-- instead of silently dropping deduplication and accounting after GC has run.
CREATE TABLE application_gc_rollback_guard (empty INTEGER CHECK (empty = 1));
INSERT INTO application_gc_rollback_guard SELECT NOT EXISTS(SELECT 1 FROM application_tombstones);
DROP TABLE application_gc_rollback_guard;

CREATE TABLE application_budget_reservations_with_application (
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
INSERT INTO application_budget_reservations_with_application SELECT * FROM application_budget_reservations;
DROP TABLE application_budget_reservations;
ALTER TABLE application_budget_reservations_with_application RENAME TO application_budget_reservations;
CREATE INDEX application_budget_state_idx
    ON application_budget_reservations(profile_id, platform, window_start, state);

CREATE TABLE application_pacing_reservations_with_application (
    application_id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    scheduled_at INTEGER NOT NULL,
    interval_ns INTEGER NOT NULL CHECK (interval_ns > 0),
    acquired_at INTEGER,
    FOREIGN KEY (application_id) REFERENCES applications(id)
);
INSERT INTO application_pacing_reservations_with_application SELECT * FROM application_pacing_reservations;
DROP TABLE application_pacing_reservations;
ALTER TABLE application_pacing_reservations_with_application RENAME TO application_pacing_reservations;
CREATE INDEX application_pacing_scope_idx
    ON application_pacing_reservations(profile_id, platform, scheduled_at);

CREATE TABLE application_campaign_items_with_application (
    campaign_id TEXT NOT NULL,
    application_id TEXT NOT NULL,
    route_index INTEGER NOT NULL CHECK (route_index >= 0),
    discovered_at INTEGER NOT NULL,
    PRIMARY KEY (campaign_id, application_id),
    FOREIGN KEY (campaign_id) REFERENCES application_campaigns(campaign_id),
    FOREIGN KEY (application_id) REFERENCES applications(id)
);
INSERT INTO application_campaign_items_with_application SELECT * FROM application_campaign_items;
DROP TABLE application_campaign_items;
ALTER TABLE application_campaign_items_with_application RENAME TO application_campaign_items;
CREATE INDEX application_campaign_items_route_idx
    ON application_campaign_items(campaign_id, route_index, discovered_at);

DROP TABLE application_tombstones;
