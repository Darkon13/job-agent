CREATE TABLE application_tombstones (
    application_id TEXT NOT NULL UNIQUE,
    profile_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    external_id TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (reason IN (
        'manual', 'retention_rejected', 'retention_stale'
    )),
    removed_at INTEGER NOT NULL,
    status TEXT NOT NULL,
    PRIMARY KEY (profile_id, platform, external_id)
);

CREATE INDEX application_tombstones_removed_idx
    ON application_tombstones(removed_at DESC);

CREATE TABLE application_budget_reservations_gc (
    application_id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    window_start INTEGER NOT NULL,
    window_end INTEGER NOT NULL,
    limit_value INTEGER NOT NULL CHECK (limit_value > 0),
    state TEXT NOT NULL CHECK (state IN ('reserved', 'committed', 'released')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (profile_id, platform, window_start)
        REFERENCES application_budget_buckets(profile_id, platform, window_start)
);
INSERT INTO application_budget_reservations_gc SELECT * FROM application_budget_reservations;
DROP TABLE application_budget_reservations;
ALTER TABLE application_budget_reservations_gc RENAME TO application_budget_reservations;
CREATE INDEX application_budget_state_idx
    ON application_budget_reservations(profile_id, platform, window_start, state);

CREATE TABLE application_pacing_reservations_gc (
    application_id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    scheduled_at INTEGER NOT NULL,
    interval_ns INTEGER NOT NULL CHECK (interval_ns > 0),
    acquired_at INTEGER
);
INSERT INTO application_pacing_reservations_gc SELECT * FROM application_pacing_reservations;
DROP TABLE application_pacing_reservations;
ALTER TABLE application_pacing_reservations_gc RENAME TO application_pacing_reservations;
CREATE INDEX application_pacing_scope_idx
    ON application_pacing_reservations(profile_id, platform, scheduled_at);

CREATE TABLE application_campaign_items_gc (
    campaign_id TEXT NOT NULL,
    application_id TEXT NOT NULL,
    route_index INTEGER NOT NULL CHECK (route_index >= 0),
    discovered_at INTEGER NOT NULL,
    PRIMARY KEY (campaign_id, application_id),
    FOREIGN KEY (campaign_id) REFERENCES application_campaigns(campaign_id)
);
INSERT INTO application_campaign_items_gc SELECT * FROM application_campaign_items;
DROP TABLE application_campaign_items;
ALTER TABLE application_campaign_items_gc RENAME TO application_campaign_items;
CREATE INDEX application_campaign_items_route_idx
    ON application_campaign_items(campaign_id, route_index, discovered_at);

-- Accounting references may now target either a live object or its tombstone,
-- but arbitrary dangling IDs remain invalid.
CREATE TRIGGER application_budget_target_insert BEFORE INSERT ON application_budget_reservations
WHEN NOT EXISTS(SELECT 1 FROM applications WHERE id = NEW.application_id)
 AND NOT EXISTS(SELECT 1 FROM application_tombstones WHERE application_id = NEW.application_id)
BEGIN SELECT RAISE(ABORT, 'application budget target not found'); END;
CREATE TRIGGER application_pacing_target_insert BEFORE INSERT ON application_pacing_reservations
WHEN NOT EXISTS(SELECT 1 FROM applications WHERE id = NEW.application_id)
 AND NOT EXISTS(SELECT 1 FROM application_tombstones WHERE application_id = NEW.application_id)
BEGIN SELECT RAISE(ABORT, 'application pacing target not found'); END;
CREATE TRIGGER application_campaign_target_insert BEFORE INSERT ON application_campaign_items
WHEN NOT EXISTS(SELECT 1 FROM applications WHERE id = NEW.application_id)
 AND NOT EXISTS(SELECT 1 FROM application_tombstones WHERE application_id = NEW.application_id)
BEGIN SELECT RAISE(ABORT, 'campaign application target not found'); END;
