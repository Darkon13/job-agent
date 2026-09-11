CREATE TABLE answer_block_revisions (
    block_tag TEXT NOT NULL,
    revision INTEGER NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    platform TEXT NOT NULL,
    source TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY(block_tag, revision)
);

CREATE TABLE answer_block_revision_answers (
    block_tag TEXT NOT NULL,
    revision INTEGER NOT NULL,
    question TEXT NOT NULL,
    question_fingerprint TEXT NOT NULL DEFAULT '',
    selected_options BLOB NOT NULL,
    text TEXT NOT NULL,
    position INTEGER NOT NULL,
    PRIMARY KEY(block_tag, revision, question),
    FOREIGN KEY(block_tag, revision) REFERENCES answer_block_revisions(block_tag, revision) ON DELETE CASCADE
);

CREATE INDEX answer_block_revisions_platform_idx ON answer_block_revisions(platform, source);
