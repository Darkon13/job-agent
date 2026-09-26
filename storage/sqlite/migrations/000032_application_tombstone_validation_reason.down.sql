DROP TRIGGER IF EXISTS application_budget_target_insert;
DROP TRIGGER IF EXISTS application_pacing_target_insert;
DROP TRIGGER IF EXISTS application_campaign_target_insert;

CREATE TABLE application_tombstones_legacy (
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
INSERT INTO application_tombstones_legacy (
    application_id, profile_id, platform, external_id, reason, removed_at, status
)
SELECT application_id, profile_id, platform, external_id, reason, removed_at, status
FROM application_tombstones
WHERE reason <> 'retention_validation';
DROP TABLE application_tombstones;
ALTER TABLE application_tombstones_legacy RENAME TO application_tombstones;
CREATE INDEX application_tombstones_removed_idx
    ON application_tombstones(removed_at DESC);

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
