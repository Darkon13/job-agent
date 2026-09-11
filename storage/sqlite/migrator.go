package sqlite

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	migrate "github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

const LatestSchemaVersion uint = 23

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Migrator struct {
	instance *migrate.Migrate
}

type MigrationVersion struct {
	Version uint
	Dirty   bool
	Present bool
}

func OpenMigrator(path string) (*Migrator, error) {
	if path == "" {
		return nil, errors.New("sqlite path is required")
	}
	path, err := prepareDatabasePath(path)
	if err != nil {
		return nil, err
	}
	dsn, err := dataSourceName(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite migrator database: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite migrator database: %w", err)
	}
	if err := adoptLegacySchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	source, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	driver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{DatabaseName: path})
	if err != nil {
		_ = source.Close()
		_ = db.Close()
		return nil, fmt.Errorf("create sqlite migration driver: %w", err)
	}
	instance, err := migrate.NewWithInstance("iofs", source, "sqlite", driver)
	if err != nil {
		_ = source.Close()
		_ = driver.Close()
		return nil, fmt.Errorf("create migrator: %w", err)
	}
	return &Migrator{instance: instance}, nil
}

// adoptLegacySchema converts databases created by the pre-golang-migrate
// prototype. It runs only in the explicit migration entrypoint and refuses to
// trust PRAGMA user_version unless the expected tables/columns are present.
func adoptLegacySchema(db *sql.DB) error {
	var migrationTableExists bool
	if err := db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations')`).Scan(&migrationTableExists); err != nil {
		return fmt.Errorf("detect migration metadata: %w", err)
	}
	if migrationTableExists {
		return nil
	}
	var legacyVersion uint
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&legacyVersion); err != nil {
		return fmt.Errorf("read legacy schema version: %w", err)
	}
	if legacyVersion == 0 {
		return nil
	}
	if legacyVersion > LatestSchemaVersion {
		return fmt.Errorf("legacy schema version %d is newer than supported version %d", legacyVersion, LatestSchemaVersion)
	}
	if err := validateLegacySchema(db, legacyVersion); err != nil {
		return fmt.Errorf("refuse legacy schema adoption at version %d: %w", legacyVersion, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin legacy schema adoption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`CREATE TABLE schema_migrations (version uint64, dirty bool)`); err != nil {
		return fmt.Errorf("create migration metadata: %w", err)
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX version_unique ON schema_migrations(version)`); err != nil {
		return fmt.Errorf("index migration metadata: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, dirty) VALUES (?, false)`, legacyVersion); err != nil {
		return fmt.Errorf("record adopted schema version: %w", err)
	}
	if _, err := tx.Exec(`PRAGMA user_version = 0`); err != nil {
		return fmt.Errorf("clear legacy schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy schema adoption: %w", err)
	}
	return nil
}

func validateLegacySchema(db *sql.DB, version uint) error {
	requiredTables := []string{"vacancies", "vacancy_discoveries", "applications", "tasks"}
	if version >= 3 {
		requiredTables = append(requiredTables, "test_definitions", "test_questions", "review_sessions", "review_prompts", "review_selections")
	}
	if version >= 4 {
		requiredTables = append(requiredTables, "conversations", "conversation_messages", "conversation_follow_ups")
	}
	if version >= 5 {
		requiredTables = append(requiredTables, "scheduled_jobs")
	}
	if version >= 6 {
		requiredTables = append(requiredTables, "search_runs")
	}
	if version >= 10 {
		requiredTables = append(requiredTables, "application_campaigns", "application_campaign_items")
	}
	if version >= 17 {
		requiredTables = append(requiredTables, "application_pacing_reservations")
	}
	if version >= 18 {
		requiredTables = append(requiredTables, "application_tailorings")
	}
	for _, table := range requiredTables {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS(
			SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, table).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("required table %q is missing", table)
		}
	}
	if version >= 2 {
		for _, column := range []string{"lease_owner", "lease_token", "lease_until"} {
			var exists bool
			if err := db.QueryRow(`SELECT EXISTS(
				SELECT 1 FROM pragma_table_info('tasks') WHERE name = ?)`, column).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("required tasks column %q is missing", column)
			}
		}
	}
	return nil
}

func (migrator *Migrator) Close() error {
	if migrator == nil || migrator.instance == nil {
		return nil
	}
	sourceErr, databaseErr := migrator.instance.Close()
	return errors.Join(sourceErr, databaseErr)
}

func (migrator *Migrator) Up() error {
	if migrator == nil || migrator.instance == nil {
		return errors.New("migrator is nil")
	}
	return ignoreNoChange(migrator.instance.Up())
}

func (migrator *Migrator) Steps(steps int) error {
	if migrator == nil || migrator.instance == nil {
		return errors.New("migrator is nil")
	}
	if steps == 0 {
		return errors.New("migration steps must not be zero")
	}
	return ignoreNoChange(migrator.instance.Steps(steps))
}

func (migrator *Migrator) Force(version int) error {
	if migrator == nil || migrator.instance == nil {
		return errors.New("migrator is nil")
	}
	return migrator.instance.Force(version)
}

func (migrator *Migrator) Version() (MigrationVersion, error) {
	if migrator == nil || migrator.instance == nil {
		return MigrationVersion{}, errors.New("migrator is nil")
	}
	version, dirty, err := migrator.instance.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return MigrationVersion{}, nil
	}
	if err != nil {
		return MigrationVersion{}, err
	}
	return MigrationVersion{Version: version, Dirty: dirty, Present: true}, nil
}

func MigrateUp(path string) (resultErr error) {
	migrator, err := OpenMigrator(path)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, migrator.Close()) }()
	return migrator.Up()
}

func ignoreNoChange(err error) error {
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}
