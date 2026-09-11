CREATE TABLE auth_sessions (
    id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN (
        'created', 'waiting_identifier', 'waiting_otp', 'waiting_password',
        'waiting_captcha', 'exchanging', 'storing', 'completed', 'expired',
        'cancelled', 'failed'
    )),
    challenge TEXT,
    credential_reference TEXT NOT NULL DEFAULT '',
    credential_revision INTEGER NOT NULL DEFAULT 0,
    failure_category TEXT NOT NULL DEFAULT '',
    failure_message TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL CHECK (revision > 0),
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX auth_sessions_profile_idx ON auth_sessions(profile_id, status);

CREATE INDEX auth_sessions_status_idx ON auth_sessions(status, updated_at DESC);
