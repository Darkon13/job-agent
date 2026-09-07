package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func TestStorePersistsAndAggregatesProfileActivity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	record, err := core.NewProfileActivityRecord("hh", "primary", "resume-1", core.ProfileActivityApplicationSubmitted, "application-1", now)
	if err != nil {
		t.Fatalf("new activity: %v", err)
	}
	if created, err := store.RecordProfileActivity(ctx, record); err != nil || !created {
		t.Fatalf("record activity: created=%t err=%v", created, err)
	}
	if created, err := store.RecordProfileActivity(ctx, record); err != nil || created {
		t.Fatalf("repeat activity: created=%t err=%v", created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	records, err := store.ListProfileActivity(ctx, storage.ProfileActivityFilter{ProfileID: "primary"})
	if err != nil || len(records) != 1 || records[0].ID != record.ID {
		t.Fatalf("activity records=%#v err=%v", records, err)
	}
	counts, err := store.ProfileActivityCounts(ctx, storage.ProfileActivityFilter{ProfileID: "primary"})
	if err != nil || len(counts) != 1 || counts[0].Count != 1 || !counts[0].LastOccurredAt.Equal(now) {
		t.Fatalf("activity counts=%#v err=%v", counts, err)
	}
	stats, err := store.Stats(ctx)
	if err != nil || stats.ProfileActivity != 1 {
		t.Fatalf("stats=%#v err=%v", stats, err)
	}
}

func TestStorePersistsProfileActivitySnapshotOptionalCounters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 7, 13, 0, 0, 0, time.UTC)
	snapshot, err := core.NewProfileActivitySnapshot("hh", "primary", "resume-1", "observe-1", now)
	if err != nil {
		t.Fatalf("new snapshot: %v", err)
	}
	zero, shows, period := 0, 35, 7
	snapshot.Views = &zero
	snapshot.SearchShows = &shows
	snapshot.PeriodDays = &period
	snapshot.ScoreHidden = true
	if created, err := store.RecordProfileActivitySnapshot(ctx, snapshot); err != nil || !created {
		t.Fatalf("record snapshot: created=%t err=%v", created, err)
	}
	items, err := store.ListProfileActivitySnapshots(ctx, storage.ProfileActivitySnapshotFilter{ProfileID: "primary", Limit: 1})
	if err != nil || len(items) != 1 || items[0].Views == nil || *items[0].Views != 0 || items[0].SearchShows == nil || *items[0].SearchShows != 35 || items[0].Score != nil {
		t.Fatalf("snapshots=%#v err=%v", items, err)
	}
}
