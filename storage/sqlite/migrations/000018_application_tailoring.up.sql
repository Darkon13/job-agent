CREATE TABLE application_tailorings (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    profile_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    external_id TEXT NOT NULL,
    resume_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN (
        'planned', 'applying', 'applied', 'submitting', 'restoring',
        'restored', 'recovery_required'
    )),
    idempotency_key TEXT NOT NULL UNIQUE,
    processor_tag TEXT NOT NULL,
    processor_version TEXT NOT NULL,
    processor_input_digest TEXT NOT NULL,
    allowed_paths BLOB NOT NULL,
    baseline_digest TEXT NOT NULL,
    tailored_digest TEXT NOT NULL,
    baseline_remote_revision TEXT NOT NULL,
    baseline_observed_at INTEGER NOT NULL,
    tailored_remote_revision TEXT NOT NULL,
    baseline_state BLOB NOT NULL,
    tailored_state BLOB NOT NULL,
    apply_proposal_id TEXT NOT NULL,
    restore_proposal_id TEXT NOT NULL,
    recovery_reason TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    FOREIGN KEY (application_id) REFERENCES applications(id)
);

CREATE UNIQUE INDEX application_tailorings_active_profile_idx
    ON application_tailorings(profile_id)
    WHERE status <> 'restored';

CREATE UNIQUE INDEX application_tailorings_application_attempt_idx
    ON application_tailorings(application_id, attempt);

CREATE INDEX application_tailorings_status_idx
    ON application_tailorings(status, updated_at DESC);
