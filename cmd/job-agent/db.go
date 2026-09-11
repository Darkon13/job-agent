package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	appconfig "github.com/Darkon13/job-agent/config"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

// runDB exposes administrative database maintenance. Both commands are meant
// for a stopped server: backup is read-consistent, restore replaces the file.
func runDB(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: job-agent db backup|restore [flags]")
	}
	switch args[0] {
	case "backup":
		return runDBBackup(ctx, args[1:], output)
	case "restore":
		return runDBRestore(args[1:], output)
	default:
		return fmt.Errorf("unknown db command %q", args[0])
	}
}

func runDBBackup(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("job-agent db backup", flag.ContinueOnError)
	flags.SetOutput(output)
	configPath := flags.String("config", "./config/config.json", "path to job-agent config")
	destination := flags.String("output", "", "backup file (default: <database>.backup-<timestamp>)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := appconfig.Load(*configPath)
	if err != nil {
		return err
	}
	databasePath := strings.TrimSpace(cfg.Database.Path)
	if databasePath == "" {
		return errors.New("database path is not configured")
	}
	outputPath := strings.TrimSpace(*destination)
	if outputPath == "" {
		outputPath = databasePath + ".backup-" + time.Now().UTC().Format("20060102T150405Z")
	}
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Backup(ctx, outputPath); err != nil {
		return err
	}
	version, err := storesqlite.VerifyDatabase(outputPath)
	if err != nil {
		return fmt.Errorf("verify backup: %w", err)
	}
	fmt.Fprintf(output, "BACKUP database=%s output=%s schema=%d\n", databasePath, outputPath, version)
	return nil
}

func runDBRestore(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("job-agent db restore", flag.ContinueOnError)
	flags.SetOutput(output)
	configPath := flags.String("config", "./config/config.json", "path to job-agent config")
	source := flags.String("input", "", "backup file to restore")
	force := flags.Bool("force", false, "replace the current database")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*source) == "" {
		return errors.New("usage: job-agent db restore --config <config.json> --input <backup> [--force]")
	}
	if _, err := storesqlite.VerifyDatabase(*source); err != nil {
		return fmt.Errorf("reject restore source: %w", err)
	}
	cfg, err := appconfig.Load(*configPath)
	if err != nil {
		return err
	}
	databasePath := strings.TrimSpace(cfg.Database.Path)
	if databasePath == "" {
		return errors.New("database path is not configured")
	}
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(absolute); err == nil && !*force {
		return fmt.Errorf("database %s already exists; stop the server and pass --force to replace it", absolute)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return err
	}
	temporary := absolute + ".restore-" + time.Now().UTC().Format("20060102T150405Z") + ".tmp"
	if err := copyFile(*source, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, absolute); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	// A stale WAL or SHM from the replaced database would corrupt the restored
	// file, so they are removed together with the old copy.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(absolute + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	fmt.Fprintf(output, "RESTORE database=%s input=%s\n", absolute, *source)
	return nil
}

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read restore source: %w", err)
	}
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		return fmt.Errorf("write restore target: %w", err)
	}
	return nil
}
