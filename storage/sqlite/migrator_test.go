package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

func TestTaskPriorityMigrationPreservesExistingTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	migrator, err := storesqlite.OpenMigrator(path)
	if err != nil {
		t.Fatalf("open migrator: %v", err)
	}
	if err := migrator.Steps(int(storesqlite.LatestSchemaVersion) - 1); err != nil {
		t.Fatalf("migrate to previous version: %v", err)
	}
	if err := migrator.Close(); err != nil {
		t.Fatalf("close previous migrator: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open previous database: %v", err)
	}
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC).UnixNano()
	if _, err := db.Exec(`INSERT INTO tasks
		(id, type, status, idempotency_key, source, platform, profile_id,
		 correlation_id, payload, attempts, available_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"task-existing", "resume.touch", "new", "existing-key", "test", "hh", "primary",
		"correlation-existing", []byte(`{}`), 0, now, now, now); err != nil {
		t.Fatalf("insert pre-priority task: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close previous database: %v", err)
	}

	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("apply priority migration: %v", err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer db.Close()
	var priority int
	if err := db.QueryRow(`SELECT priority FROM tasks WHERE id = ?`, "task-existing").Scan(&priority); err != nil {
		t.Fatalf("read migrated priority: %v", err)
	}
	if priority != 0 {
		t.Fatalf("migrated priority=%d, want 0", priority)
	}
}

func TestPreparationProvenanceMigrationPreservesExistingApplications(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	migrator, err := storesqlite.OpenMigrator(path)
	if err != nil {
		t.Fatalf("open migrator: %v", err)
	}
	if err := migrator.Steps(int(storesqlite.LatestSchemaVersion) - 1); err != nil {
		t.Fatalf("migrate to previous version: %v", err)
	}
	if err := migrator.Close(); err != nil {
		t.Fatalf("close previous migrator: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open previous database: %v", err)
	}
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC).UnixNano()
	if _, err := db.Exec(`INSERT INTO vacancies
		(platform, external_id, url, title, employer, state, observed_at)
		VALUES ('hh', '42', '', 'Backend', '', 'open', ?)`, now); err != nil {
		t.Fatalf("insert existing vacancy: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO applications
		(id, profile_id, platform, external_id, status, attempts, external_negotiation_id,
		 failure_category, failure_message, decision_code, decision_reason, prepared_resume_id,
		 prepared_message, created_at, updated_at)
		VALUES ('application-existing', 'primary', 'hh', '42', 'new', 0, '', '', '', '', '', '', '', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert existing application: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close previous database: %v", err)
	}

	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("apply provenance migration: %v", err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer store.Close()
	application, err := store.Application(context.Background(), core.ApplicationKey{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	})
	if err != nil || !application.PreparationProvenance.IsZero() {
		t.Fatalf("migrated application=%#v err=%v", application, err)
	}
}

func TestStoreRequiresExplicitMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	if _, err := storesqlite.Open(path); !errors.Is(err, storesqlite.ErrSchemaNotReady) {
		t.Fatalf("open unmigrated store error = %v, want schema not ready", err)
	}
}

func TestMigratorAppliesAndRevertsVersionedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	migrator, err := storesqlite.OpenMigrator(path)
	if err != nil {
		t.Fatalf("open migrator: %v", err)
	}
	version, err := migrator.Version()
	if err != nil || version.Present {
		t.Fatalf("initial version: %#v err=%v", version, err)
	}
	if err := migrator.Steps(1); err != nil {
		t.Fatalf("apply first migration: %v", err)
	}
	version, err = migrator.Version()
	if err != nil || !version.Present || version.Version != 1 || version.Dirty {
		t.Fatalf("version after first step: %#v err=%v", version, err)
	}
	if _, err := storesqlite.Open(path); !errors.Is(err, storesqlite.ErrSchemaNotReady) {
		t.Fatalf("open partially migrated store error = %v, want schema not ready", err)
	}
	if err := migrator.Up(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	version, err = migrator.Version()
	if err != nil || version.Version != storesqlite.LatestSchemaVersion || version.Dirty {
		t.Fatalf("latest version: %#v err=%v", version, err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := migrator.Steps(-1); err != nil {
		t.Fatalf("revert one migration: %v", err)
	}
	if _, err := storesqlite.Open(path); !errors.Is(err, storesqlite.ErrSchemaNotReady) {
		t.Fatalf("open reverted store error = %v, want schema not ready", err)
	}
	if err := migrator.Up(); err != nil {
		t.Fatalf("restore latest migration: %v", err)
	}
	if err := migrator.Close(); err != nil {
		t.Fatalf("close migrator: %v", err)
	}
}

func TestMigratorAdoptsValidatedLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job-agent.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE vacancies(id TEXT);
		CREATE TABLE vacancy_discoveries(id TEXT);
		CREATE TABLE applications(id TEXT);
		CREATE TABLE tasks(
			id TEXT,
			type TEXT,
			status TEXT,
			available_at INTEGER,
			created_at INTEGER,
			lease_owner TEXT,
			lease_token TEXT,
			lease_until INTEGER
		);
		CREATE INDEX tasks_available_idx ON tasks(status, available_at);
		PRAGMA user_version = 2;
	`)
	if err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}
	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("adopt and migrate legacy schema: %v", err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open adopted store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close adopted store: %v", err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen adopted database: %v", err)
	}
	defer db.Close()
	var legacyVersion uint
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&legacyVersion); err != nil {
		t.Fatalf("read cleared legacy version: %v", err)
	}
	if legacyVersion != 0 {
		t.Fatalf("legacy user_version was not cleared: %d", legacyVersion)
	}
}
