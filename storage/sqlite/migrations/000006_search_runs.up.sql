CREATE TABLE search_runs (
    search_id TEXT PRIMARY KEY,
    adapter TEXT NOT NULL,
    platform TEXT NOT NULL,
    search_profile_id TEXT NOT NULL,
    target_profiles BLOB NOT NULL,
    query BLOB NOT NULL,
    correlation_id TEXT NOT NULL,
    cursor TEXT NOT NULL,
    done INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
