CREATE TABLE runtime_instances (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    owner TEXT NOT NULL,
    acquired_at INTEGER NOT NULL,
    renewed_at INTEGER NOT NULL
);
