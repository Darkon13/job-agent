package scheduler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/scheduler"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

type mutableClock struct{ now time.Time }

func (clock *mutableClock) Now() time.Time { return clock.now }

type sequenceIDs struct{ next int }

func (ids *sequenceIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}

func TestSchedulerPersistsNextRunAppliesJitterAndCollapsesMisfires(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "job-agent.db")
	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := &mutableClock{now: time.Date(2026, 7, 19, 10, 30, 0, 0, time.UTC)}
	service, err := scheduler.New(store, store, clock, &sequenceIDs{})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	payload, _ := json.Marshal(map[string]string{"profile_id": "primary", "resume_id": "resume-1"})
	definition := scheduler.Definition{
		JobTag: "touch-primary", TriggerIndex: 0, Expression: "0 * * * *", Timezone: "UTC",
		ActionType: core.TaskResumeTouch, Platform: "hh", ProfileID: "primary", Payload: payload,
		JitterMin: 5 * time.Minute, JitterMax: 10 * time.Minute,
	}
	if err := service.Sync(ctx, []scheduler.Definition{definition}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	clock.now = time.Date(2026, 7, 19, 11, 0, 0, 0, time.UTC)
	if count, err := service.ReconcileDue(ctx); err != nil || count != 1 {
		t.Fatalf("first reconcile: count=%d err=%v", count, err)
	}
	if lease, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "resume-worker", TaskType: core.TaskResumeTouch, Now: clock.now.Add(4 * time.Minute), LeaseDuration: time.Minute}); err != nil || found {
		t.Fatalf("task became available before jitter minimum: lease=%#v found=%t err=%v", lease, found, err)
	}
	lease, found, err := store.Claim(ctx, broker.ClaimParams{WorkerID: "resume-worker", TaskType: core.TaskResumeTouch, Now: clock.now.Add(11 * time.Minute), LeaseDuration: time.Minute})
	if err != nil || !found || lease.Task.Type != core.TaskResumeTouch {
		t.Fatalf("task unavailable after jitter maximum: lease=%#v found=%t err=%v", lease, found, err)
	}
	if err := store.Complete(ctx, lease, clock.now.Add(11*time.Minute+time.Second)); err != nil {
		t.Fatalf("complete first task: %v", err)
	}

	// Three missed hourly ticks collapse into one run_once task. The next tick is
	// computed after current time instead of replaying the whole backlog.
	clock.now = time.Date(2026, 7, 19, 14, 30, 0, 0, time.UTC)
	if count, err := service.ReconcileDue(ctx); err != nil || count != 1 {
		t.Fatalf("misfire reconcile: count=%d err=%v", count, err)
	}
	stats, err := store.Stats(ctx)
	if err != nil || stats.Tasks != 2 {
		t.Fatalf("misfire task count: stats=%#v err=%v", stats, err)
	}
	if due, err := store.DueSchedules(ctx, clock.now, 10); err != nil || len(due) != 0 {
		t.Fatalf("schedule did not advance beyond now: due=%#v err=%v", due, err)
	}

	if err := service.Sync(ctx, nil); err != nil {
		t.Fatalf("disable schedules: %v", err)
	}
	clock.now = clock.now.Add(24 * time.Hour)
	if count, err := service.ReconcileDue(ctx); err != nil || count != 0 {
		t.Fatalf("disabled reconcile: count=%d err=%v", count, err)
	}
}
