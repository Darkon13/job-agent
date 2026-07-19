package sqlite_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

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
			lease_owner TEXT,
			lease_token TEXT,
			lease_until INTEGER
		);
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
