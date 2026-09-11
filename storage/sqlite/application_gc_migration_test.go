package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

func TestGCMigrationPreservesExistingBudgetAcrossRollbackAndUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gc.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	v := core.Vacancy{Platform: "hh", ExternalID: "v", Title: "Go", State: core.VacancyStateOpen, ObservedAt: now}
	if _, err := store.UpsertVacancy(ctx, v); err != nil {
		t.Fatal(err)
	}
	a, _ := core.NewApplication("a", core.ApplicationKey{ProfileID: "primary", Vacancy: v.Key()}, now)
	if _, _, err := store.CreateApplication(ctx, a); err != nil {
		t.Fatal(err)
	}
	params := core.ReserveApplicationBudgetParams{ApplicationID: a.ID, ProfileID: "primary", Platform: "hh", WindowStart: now.Truncate(24 * time.Hour), WindowEnd: now.Truncate(24 * time.Hour).Add(24 * time.Hour), Limit: 1, Now: now}
	if _, err := store.ReserveApplicationBudget(ctx, params); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := storesqlite.OpenMigrator(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Steps(18 - int(storesqlite.LatestSchemaVersion)); err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storesqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reservation, err := store.ReserveApplicationBudget(ctx, params)
	if err != nil || reservation.State != core.ApplicationBudgetReserved {
		t.Fatalf("reservation lost: %v %v", reservation, err)
	}
	if err := store.CommitApplicationBudget(ctx, a.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := a.Transition(core.ApplicationPreparing, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := a.Transition(core.ApplicationDryRun, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveApplication(ctx, a, core.ApplicationNew); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RemoveApplication(ctx, a.ID, core.ApplicationRemoval{Reason: core.ApplicationRemovalManual}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = storesqlite.OpenMigrator(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Steps(18 - int(storesqlite.LatestSchemaVersion)); err == nil {
		t.Fatal("destructive rollback was allowed after GC")
	}
	_ = m.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM application_tombstones").Scan(&count); err != nil || count != 1 {
		t.Fatalf("tombstone lost on failed rollback: %d %v", count, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM application_budget_reservations WHERE state = 'committed'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("budget lost on failed rollback: %d %v", count, err)
	}
}
