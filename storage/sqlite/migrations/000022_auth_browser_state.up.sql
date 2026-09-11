ALTER TABLE auth_sessions ADD COLUMN browser_state_reference TEXT NOT NULL DEFAULT '';

ALTER TABLE auth_sessions ADD COLUMN browser_state_digest TEXT NOT NULL DEFAULT '';
