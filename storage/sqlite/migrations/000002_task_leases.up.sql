ALTER TABLE tasks ADD COLUMN lease_owner TEXT;
ALTER TABLE tasks ADD COLUMN lease_token TEXT;
ALTER TABLE tasks ADD COLUMN lease_until INTEGER;
CREATE INDEX tasks_lease_idx ON tasks(status, lease_until);
