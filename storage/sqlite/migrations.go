package sqlite

var migrations = []string{
	`CREATE TABLE vacancies (
		platform TEXT NOT NULL,
		external_id TEXT NOT NULL,
		url TEXT NOT NULL,
		title TEXT NOT NULL,
		employer TEXT NOT NULL,
		state TEXT NOT NULL,
		published_at INTEGER,
		observed_at INTEGER NOT NULL,
		attributes BLOB,
		PRIMARY KEY (platform, external_id)
	);

	CREATE TABLE vacancy_discoveries (
		platform TEXT NOT NULL,
		external_id TEXT NOT NULL,
		search_id TEXT NOT NULL,
		profile_id TEXT NOT NULL,
		discovered_at INTEGER NOT NULL,
		PRIMARY KEY (platform, external_id, search_id, profile_id),
		FOREIGN KEY (platform, external_id) REFERENCES vacancies(platform, external_id)
	);

	CREATE TABLE applications (
		id TEXT PRIMARY KEY,
		profile_id TEXT NOT NULL,
		platform TEXT NOT NULL,
		external_id TEXT NOT NULL,
		status TEXT NOT NULL,
		attempts INTEGER NOT NULL,
		external_negotiation_id TEXT NOT NULL,
		failure_category TEXT NOT NULL,
		failure_message TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		submitted_at INTEGER,
		UNIQUE (profile_id, platform, external_id),
		FOREIGN KEY (platform, external_id) REFERENCES vacancies(platform, external_id)
	);

	CREATE TABLE tasks (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		status TEXT NOT NULL,
		idempotency_key TEXT NOT NULL UNIQUE,
		source TEXT NOT NULL,
		platform TEXT NOT NULL,
		profile_id TEXT NOT NULL,
		correlation_id TEXT NOT NULL,
		payload BLOB NOT NULL,
		attempts INTEGER NOT NULL,
		available_at INTEGER NOT NULL,
		deadline INTEGER,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		failure_category TEXT,
		failure_message TEXT
	);

	CREATE INDEX tasks_available_idx ON tasks(status, available_at);
	CREATE INDEX discoveries_search_idx ON vacancy_discoveries(search_id, discovered_at);`,
	`ALTER TABLE tasks ADD COLUMN lease_owner TEXT;
	ALTER TABLE tasks ADD COLUMN lease_token TEXT;
	ALTER TABLE tasks ADD COLUMN lease_until INTEGER;
	CREATE INDEX tasks_lease_idx ON tasks(status, lease_until);`,
}
