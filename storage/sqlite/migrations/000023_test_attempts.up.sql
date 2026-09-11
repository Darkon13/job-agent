CREATE TABLE test_attempts (
    platform TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    external_id TEXT NOT NULL,
    test_definition_id TEXT NOT NULL,
    status TEXT NOT NULL,
    attempt_fingerprint TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL,
    observed_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(platform, profile_id, external_id),
    FOREIGN KEY(test_definition_id) REFERENCES test_definitions(id)
);

CREATE INDEX test_attempts_definition_idx ON test_attempts(test_definition_id);
