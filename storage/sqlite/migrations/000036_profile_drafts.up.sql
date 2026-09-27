CREATE TABLE profile_drafts (
    tag TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    adapter TEXT NOT NULL,
    state_file TEXT NOT NULL,
    identity TEXT,
    resumes TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    revision INTEGER NOT NULL
);
