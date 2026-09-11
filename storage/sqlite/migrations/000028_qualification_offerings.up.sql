CREATE TABLE qualification_offerings (
    platform TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    offering_id TEXT NOT NULL,
    external_id TEXT NOT NULL,
    family_id TEXT NOT NULL,
    family_name TEXT NOT NULL,
    level_id TEXT NOT NULL,
    level_name TEXT NOT NULL,
    level_order INTEGER,
    status TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(platform, profile_id, offering_id)
);

CREATE INDEX qualification_offerings_profile_idx
    ON qualification_offerings(platform, profile_id, family_id, level_order, level_id);
