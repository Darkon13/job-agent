package sqlite_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/scheduler"
)

func TestSyncSchedulesPrunesDisabledLeftovers(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	entry := func(tag string, next time.Time) scheduler.Entry {
		return scheduler.Entry{
			Definition: scheduler.Definition{
				JobTag: tag, TriggerIndex: 0, Interval: time.Hour, ActionType: core.TaskApplicationCampaign,
				Platform: "hh", ProfileID: "primary", Payload: json.RawMessage("{}"),
			},
			NextRunAt: next,
		}
	}
	if err := store.SyncSchedules(ctx, []scheduler.Entry{entry("keep", now.Add(time.Hour)), entry("gone", now.Add(time.Hour))}, now); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	// The next sync no longer lists "gone": its row must not stay behind as a
	// disabled historical entry.
	if err := store.SyncSchedules(ctx, []scheduler.Entry{entry("keep", now.Add(2*time.Hour))}, now.Add(time.Minute)); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	schedules, err := store.Schedules(ctx)
	if err != nil {
		t.Fatalf("schedules: %v", err)
	}
	if len(schedules) != 1 || schedules[0].JobTag != "keep" {
		t.Fatalf("schedules = %#v", schedules)
	}
}
