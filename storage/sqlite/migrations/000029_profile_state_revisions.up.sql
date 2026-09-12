CREATE TABLE profile_state_revisions (
    proposal_id TEXT PRIMARY KEY REFERENCES profile_state_proposals(id),
    resource_tag TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    manifest_digest TEXT NOT NULL,
    observed_digest TEXT NOT NULL,
    desired_digest TEXT NOT NULL,
    remote_revision TEXT NOT NULL,
    changes BLOB NOT NULL,
    source TEXT NOT NULL,
    applied_at INTEGER NOT NULL
);

CREATE INDEX profile_state_revisions_resource_idx
    ON profile_state_revisions(resource_tag, applied_at DESC);
CREATE INDEX profile_state_revisions_profile_idx
    ON profile_state_revisions(profile_id, applied_at DESC);
