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
	`CREATE TABLE test_definitions (
		id TEXT PRIMARY KEY,
		platform TEXT NOT NULL,
		external_id TEXT NOT NULL,
		title TEXT NOT NULL,
		family_id TEXT NOT NULL,
		family_name TEXT NOT NULL,
		level_id TEXT NOT NULL,
		level_name TEXT NOT NULL,
		level_order INTEGER,
		last_attempt_fingerprint TEXT NOT NULL,
		observed_attempts INTEGER NOT NULL,
		discovered_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		UNIQUE(platform, external_id, family_id, level_id)
	);

	CREATE TABLE test_questions (
		test_definition_id TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		text TEXT NOT NULL,
		kind TEXT NOT NULL,
		options BLOB NOT NULL,
		first_seen_at INTEGER NOT NULL,
		last_seen_at INTEGER NOT NULL,
		PRIMARY KEY(test_definition_id, fingerprint),
		FOREIGN KEY(test_definition_id) REFERENCES test_definitions(id) ON DELETE CASCADE
	);

	CREATE TABLE review_sessions (
		id TEXT PRIMARY KEY,
		test_definition_id TEXT NOT NULL,
		platform TEXT NOT NULL,
		profile_id TEXT NOT NULL,
		correlation_id TEXT NOT NULL,
		status TEXT NOT NULL,
		revision INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		FOREIGN KEY(test_definition_id) REFERENCES test_definitions(id)
	);

	CREATE TABLE review_prompts (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		revision INTEGER NOT NULL,
		question BLOB NOT NULL,
		deadline INTEGER,
		created_at INTEGER NOT NULL,
		UNIQUE(session_id, revision),
		FOREIGN KEY(session_id) REFERENCES review_sessions(id) ON DELETE CASCADE
	);

	CREATE TABLE review_selections (
		session_id TEXT NOT NULL,
		revision INTEGER NOT NULL,
		prompt_id TEXT NOT NULL,
		selected_options BLOB NOT NULL,
		text TEXT NOT NULL,
		source TEXT NOT NULL,
		assessment TEXT NOT NULL,
		selected_at INTEGER NOT NULL,
		PRIMARY KEY(session_id, revision),
		FOREIGN KEY(session_id) REFERENCES review_sessions(id) ON DELETE CASCADE,
		FOREIGN KEY(prompt_id) REFERENCES review_prompts(id)
	);

	CREATE INDEX test_definitions_catalog_idx ON test_definitions(platform, family_id, level_id, updated_at);
	CREATE INDEX test_questions_seen_idx ON test_questions(test_definition_id, last_seen_at);
	CREATE INDEX review_sessions_status_idx ON review_sessions(status, updated_at);`,
}
