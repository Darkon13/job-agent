package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreSerializesRuntimeInstanceLease(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	acquired, err := store.AcquireRuntimeInstance(ctx, "instance-a", now, 90*time.Second)
	if err != nil || !acquired {
		t.Fatalf("first acquire: acquired=%v err=%v", acquired, err)
	}
	if acquired, err := store.AcquireRuntimeInstance(ctx, "instance-b", now.Add(10*time.Second), 90*time.Second); err != nil || acquired {
		t.Fatalf("second acquire: acquired=%v err=%v", acquired, err)
	}
	if renewed, err := store.RenewRuntimeInstance(ctx, "instance-b", now); err != nil || renewed {
		t.Fatalf("foreign renew: renewed=%v err=%v", renewed, err)
	}
	if renewed, err := store.RenewRuntimeInstance(ctx, "instance-a", now.Add(30*time.Second)); err != nil || !renewed {
		t.Fatalf("owner renew: renewed=%v err=%v", renewed, err)
	}
	if acquired, err := store.AcquireRuntimeInstance(ctx, "instance-b", now.Add(4*time.Minute), 90*time.Second); err != nil || !acquired {
		t.Fatalf("stale takeover: acquired=%v err=%v", acquired, err)
	}
	if renewed, err := store.RenewRuntimeInstance(ctx, "instance-a", now.Add(3*time.Minute)); err != nil || renewed {
		t.Fatalf("renew after takeover: renewed=%v err=%v", renewed, err)
	}
	if err := store.ReleaseRuntimeInstance(ctx, "instance-b"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if acquired, err := store.AcquireRuntimeInstance(ctx, "instance-c", now.Add(4*time.Minute), 90*time.Second); err != nil || !acquired {
		t.Fatalf("acquire after release: acquired=%v err=%v", acquired, err)
	}
	if err := store.ReleaseRuntimeInstance(ctx, "instance-b"); err != nil {
		t.Fatalf("foreign release: %v", err)
	}
	if acquired, err := store.AcquireRuntimeInstance(ctx, "instance-d", now.Add(4*time.Minute), 90*time.Second); err != nil || acquired {
		t.Fatalf("foreign release dropped the lease: acquired=%v err=%v", acquired, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	if acquired, err := store.AcquireRuntimeInstance(ctx, "instance-e", now.Add(4*time.Minute), 90*time.Second); err != nil || acquired {
		t.Fatalf("lease did not survive reopen: acquired=%v err=%v", acquired, err)
	}
	if acquired, err := store.AcquireRuntimeInstance(ctx, "instance-e", now.Add(6*time.Minute), 90*time.Second); err != nil || !acquired {
		t.Fatalf("takeover after ttl: acquired=%v err=%v", acquired, err)
	}
}
