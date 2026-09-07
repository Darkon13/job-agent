CREATE TABLE profile_activity (
    id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    resume_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN (
        'vacancy.inspected',
        'application.submitted',
        'conversation.message_sent',
        'resume.touched'
    )),
    source_id TEXT NOT NULL,
    occurred_at INTEGER NOT NULL,
    UNIQUE(platform, profile_id, kind, source_id)
);

CREATE INDEX profile_activity_profile_time_idx
    ON profile_activity(profile_id, occurred_at DESC);
CREATE INDEX profile_activity_kind_time_idx
    ON profile_activity(kind, occurred_at DESC);

CREATE TABLE profile_activity_snapshots (
    id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    resume_id TEXT NOT NULL,
    source_id TEXT NOT NULL,
    score INTEGER,
    score_hidden INTEGER NOT NULL CHECK (score_hidden IN (0, 1)),
    period_days INTEGER,
    search_shows INTEGER,
    views INTEGER,
    new_views INTEGER,
    invitations INTEGER,
    new_invitations INTEGER,
    response_streak INTEGER,
    responses_required INTEGER,
    observed_at INTEGER NOT NULL,
    UNIQUE(platform, profile_id, resume_id, source_id)
);

CREATE INDEX profile_activity_snapshots_profile_time_idx
    ON profile_activity_snapshots(profile_id, resume_id, observed_at DESC);
