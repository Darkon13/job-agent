-- The account summary captured after a sign-in for profiles that live in the
-- read-only configuration file: the operator sees the account in the dashboard
-- without editing the config.
CREATE TABLE profile_identities (
    profile_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    phone TEXT NOT NULL DEFAULT '',
    account_hash TEXT NOT NULL DEFAULT '',
    captured_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
