DROP INDEX tasks_lease_idx;
ALTER TABLE tasks DROP COLUMN lease_until;
ALTER TABLE tasks DROP COLUMN lease_token;
ALTER TABLE tasks DROP COLUMN lease_owner;
