package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Backup writes a consistent compact copy of the database. VACUUM INTO does
// not need a stopped writer and never includes the source WAL; an existing
// destination is never overwritten.
func (store *Store) Backup(ctx context.Context, destination string) error {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return errors.New("database backup requires destination")
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve backup destination: %w", err)
	}
	if _, err := os.Lstat(absolute); err == nil {
		return fmt.Errorf("backup destination %s already exists", absolute)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	quoted := strings.ReplaceAll(absolute, "'", "''")
	if _, err := store.db.ExecContext(ctx, "VACUUM INTO '"+quoted+"'"); err != nil {
		return fmt.Errorf("backup database: %w", err)
	}
	return nil
}

// VerifyDatabase checks that path is an intact SQLite database on this schema
// line and returns its schema version. It opens the file read-only.
func VerifyDatabase(path string) (uint, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, errors.New("database verification requires path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return 0, fmt.Errorf("resolve database path: %w", err)
	}
	if _, err := os.Stat(absolute); err != nil {
		return 0, fmt.Errorf("open database: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return 0, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check(1)").Scan(&integrity); err != nil {
		return 0, fmt.Errorf("check database integrity: %w", err)
	}
	if integrity != "ok" {
		return 0, fmt.Errorf("database integrity check failed: %s", integrity)
	}
	var version uint
	var dirty bool
	if err := db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("database has no migration record")
		}
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if dirty {
		return 0, fmt.Errorf("database schema is dirty at version %d", version)
	}
	if version > LatestSchemaVersion {
		return 0, fmt.Errorf("database schema %d is newer than supported %d", version, LatestSchemaVersion)
	}
	return version, nil
}
