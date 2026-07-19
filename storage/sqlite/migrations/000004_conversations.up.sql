CREATE TABLE conversations (
    id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    external_id TEXT NOT NULL,
    application_id TEXT NOT NULL,
    status TEXT NOT NULL,
    last_message_id TEXT NOT NULL,
    last_message_at INTEGER,
    last_incoming_at INTEGER,
    last_outgoing_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    UNIQUE(platform, profile_id, external_id)
);

CREATE TABLE conversation_messages (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    external_id TEXT NOT NULL,
    reply_to_id TEXT NOT NULL,
    direction TEXT NOT NULL,
    kind TEXT NOT NULL,
    status TEXT NOT NULL,
    text TEXT NOT NULL,
    options BLOB NOT NULL,
    occurred_at INTEGER NOT NULL,
    FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX conversation_messages_external_idx
    ON conversation_messages(conversation_id, external_id)
    WHERE external_id <> '';
CREATE INDEX conversation_messages_timeline_idx
    ON conversation_messages(conversation_id, occurred_at, id);

CREATE TABLE conversation_follow_ups (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    anchor_message_id TEXT NOT NULL,
    anchor_at INTEGER NOT NULL,
    run_at INTEGER NOT NULL,
    deadline INTEGER,
    content_text TEXT NOT NULL,
    content_template TEXT NOT NULL,
    content_operator TEXT NOT NULL,
    cancel_on_incoming INTEGER NOT NULL,
    require_active_conversation INTEGER NOT NULL,
    max_follow_ups INTEGER NOT NULL,
    cooldown INTEGER NOT NULL,
    status TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    sent_message_id TEXT NOT NULL,
    cancel_reason TEXT NOT NULL,
    failure_message TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX conversations_profile_updated_idx
    ON conversations(profile_id, status, updated_at DESC);
CREATE INDEX conversation_follow_ups_due_idx
    ON conversation_follow_ups(status, run_at);
CREATE INDEX conversation_follow_ups_conversation_idx
    ON conversation_follow_ups(conversation_id, created_at DESC);
