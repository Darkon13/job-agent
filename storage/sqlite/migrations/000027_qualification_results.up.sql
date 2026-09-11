CREATE TABLE qualification_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    platform TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    family_id TEXT NOT NULL,
    family_name TEXT NOT NULL,
    level_id TEXT NOT NULL,
    level_name TEXT NOT NULL,
    level_order INTEGER,
    status TEXT NOT NULL,
    score REAL,
    max_score REAL,
    verified INTEGER NOT NULL,
    answer_block_tag TEXT NOT NULL DEFAULT '',
    attempt_fingerprint TEXT NOT NULL DEFAULT '',
    completed_at INTEGER NOT NULL,
    recorded_at INTEGER NOT NULL
);

CREATE INDEX qualification_attempts_level_idx
    ON qualification_attempts(platform, profile_id, family_id, level_id, completed_at DESC);

CREATE TABLE qualification_best_results (
    platform TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    family_id TEXT NOT NULL,
    level_id TEXT NOT NULL,
    status TEXT NOT NULL,
    score REAL,
    max_score REAL,
    verified INTEGER NOT NULL,
    answer_block_tag TEXT NOT NULL DEFAULT '',
    completed_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(platform, profile_id, family_id, level_id)
);
