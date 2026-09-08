ALTER TABLE tasks
    ADD COLUMN priority INTEGER NOT NULL DEFAULT 0
    CHECK (priority BETWEEN -1000 AND 1000);

ALTER TABLE scheduled_jobs
    ADD COLUMN priority INTEGER NOT NULL DEFAULT 0
    CHECK (priority BETWEEN -1000 AND 1000);

DROP INDEX IF EXISTS tasks_available_idx;
CREATE INDEX tasks_available_idx
    ON tasks(type, status, priority DESC, available_at, created_at);
