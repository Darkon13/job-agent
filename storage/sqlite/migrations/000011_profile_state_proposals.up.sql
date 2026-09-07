CREATE TABLE profile_state_proposals (
    id TEXT PRIMARY KEY,
    resource_tag TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    ownership TEXT NOT NULL CHECK (ownership = 'declared_fields'),
    status TEXT NOT NULL CHECK (status IN ('planned', 'no_changes')),
    idempotency_key TEXT NOT NULL UNIQUE,
    manifest_digest TEXT NOT NULL,
    observed_digest TEXT NOT NULL,
    desired_digest TEXT NOT NULL,
    remote_revision TEXT NOT NULL,
    desired_state BLOB NOT NULL,
    changes BLOB NOT NULL,
    observation_time INTEGER NOT NULL,
    revision INTEGER NOT NULL CHECK (revision = 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX profile_state_proposals_resource_idx
    ON profile_state_proposals(resource_tag, created_at DESC);
CREATE INDEX profile_state_proposals_profile_idx
    ON profile_state_proposals(profile_id, status, created_at DESC);
