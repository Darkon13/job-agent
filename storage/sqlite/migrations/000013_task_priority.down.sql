DROP INDEX IF EXISTS tasks_available_idx;

ALTER TABLE scheduled_jobs DROP COLUMN priority;
ALTER TABLE tasks DROP COLUMN priority;

CREATE INDEX tasks_available_idx ON tasks(status, available_at);
