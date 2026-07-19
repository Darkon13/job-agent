package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"

	appconfig "github.com/Darkon13/job-agent/config"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("job-agent-migrate", flag.ContinueOnError)
	flags.SetOutput(output)
	configPath := flags.String("config", "./config/config.json", "path to job-agent config")
	steps := flags.Int("steps", 0, "number of migrations for up/down")
	if err := flags.Parse(args); err != nil {
		return err
	}
	commandArgs := flags.Args()
	if len(commandArgs) == 0 {
		return errors.New("usage: job-agent-migrate [-config path] [-steps n] up|down|version|force [version]")
	}
	cfg, err := appconfig.Load(*configPath)
	if err != nil {
		return err
	}
	migrator, err := storesqlite.OpenMigrator(cfg.Database.Path)
	if err != nil {
		return err
	}
	defer func() {
		if err := migrator.Close(); err != nil {
			_, _ = fmt.Fprintf(output, "close migrator: %v\n", err)
		}
	}()

	switch commandArgs[0] {
	case "up":
		if len(commandArgs) != 1 || *steps < 0 {
			return errors.New("up accepts an optional non-negative -steps value")
		}
		if *steps == 0 {
			err = migrator.Up()
		} else {
			err = migrator.Steps(*steps)
		}
	case "down":
		if len(commandArgs) != 1 || *steps <= 0 {
			return errors.New("down requires an explicit positive -steps value")
		}
		err = migrator.Steps(-*steps)
	case "version":
		if len(commandArgs) != 1 || *steps != 0 {
			return errors.New("version does not accept arguments or -steps")
		}
		var version storesqlite.MigrationVersion
		version, err = migrator.Version()
		if err == nil {
			if !version.Present {
				_, err = fmt.Fprintln(output, "version: none")
			} else {
				_, err = fmt.Fprintf(output, "version: %d, dirty: %t\n", version.Version, version.Dirty)
			}
		}
	case "force":
		if len(commandArgs) != 2 || *steps != 0 {
			return errors.New("force requires exactly one version argument")
		}
		version, parseErr := strconv.Atoi(commandArgs[1])
		if parseErr != nil || version < -1 {
			return errors.New("force version must be an integer greater than or equal to -1")
		}
		err = migrator.Force(version)
	default:
		return fmt.Errorf("unknown migration command %q", commandArgs[0])
	}
	if err != nil {
		return fmt.Errorf("migration %s: %w", commandArgs[0], err)
	}
	return nil
}
