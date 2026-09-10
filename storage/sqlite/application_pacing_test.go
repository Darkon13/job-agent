package sqlite_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestStoreSpacesApplicationSubmissionsAndRecoversWithoutBurst(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	store, applications := applicationBudgetStore(t, now, 3)

	first, allowed, err := store.AcquireApplicationPace(ctx, core.AcquireApplicationPaceParams{
		ApplicationID: applications[0], ProfileID: "primary", Platform: "hh", Interval: 15 * time.Second, Now: now,
	})
	if err != nil || !allowed || !first.ScheduledAt.Equal(now) || first.AcquiredAt == nil {
		t.Fatalf("first reservation=%#v allowed=%v err=%v", first, allowed, err)
	}
	second, allowed, err := store.AcquireApplicationPace(ctx, core.AcquireApplicationPaceParams{
		ApplicationID: applications[1], ProfileID: "primary", Platform: "hh", Interval: 20 * time.Second, Now: now,
	})
	if err != nil || allowed || !second.ScheduledAt.Equal(now.Add(15*time.Second)) {
		t.Fatalf("second reservation=%#v allowed=%v err=%v", second, allowed, err)
	}
	third, allowed, err := store.AcquireApplicationPace(ctx, core.AcquireApplicationPaceParams{
		ApplicationID: applications[2], ProfileID: "primary", Platform: "hh", Interval: 25 * time.Second, Now: now,
	})
	if err != nil || allowed || !third.ScheduledAt.Equal(now.Add(35*time.Second)) {
		t.Fatalf("third reservation=%#v allowed=%v err=%v", third, allowed, err)
	}

	// After downtime, only one overdue reservation is admitted immediately.
	restartedAt := now.Add(2 * time.Hour)
	second, allowed, err = store.AcquireApplicationPace(ctx, core.AcquireApplicationPaceParams{
		ApplicationID: applications[1], ProfileID: "primary", Platform: "hh", Interval: time.Second, Now: restartedAt,
	})
	if err != nil || !allowed || second.AcquiredAt == nil || second.Interval != 20*time.Second {
		t.Fatalf("overdue second=%#v allowed=%v err=%v", second, allowed, err)
	}
	third, allowed, err = store.AcquireApplicationPace(ctx, core.AcquireApplicationPaceParams{
		ApplicationID: applications[2], ProfileID: "primary", Platform: "hh", Interval: time.Second, Now: restartedAt,
	})
	if err != nil || allowed || !third.ScheduledAt.Equal(restartedAt.Add(20*time.Second)) || third.Interval != 25*time.Second {
		t.Fatalf("rebooked third=%#v allowed=%v err=%v", third, allowed, err)
	}
}

func TestStoreSerializesConcurrentApplicationPacingReservations(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	store, applications := applicationBudgetStore(t, now, 2)
	start := make(chan struct{})
	results := make(chan struct {
		reservation core.ApplicationPaceReservation
		allowed     bool
		err         error
	}, 2)
	var workers sync.WaitGroup
	for _, applicationID := range applications {
		applicationID := applicationID
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			reservation, allowed, err := store.AcquireApplicationPace(ctx, core.AcquireApplicationPaceParams{
				ApplicationID: applicationID, ProfileID: "primary", Platform: "hh", Interval: 15 * time.Second, Now: now,
			})
			results <- struct {
				reservation core.ApplicationPaceReservation
				allowed     bool
				err         error
			}{reservation: reservation, allowed: allowed, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	allowedCount := 0
	scheduled := make(map[time.Time]struct{}, 2)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent pacing reservation: %v", result.err)
		}
		if result.allowed {
			allowedCount++
		}
		scheduled[result.reservation.ScheduledAt] = struct{}{}
	}
	if allowedCount != 1 || len(scheduled) != 2 {
		t.Fatalf("allowed=%d distinct scheduled slots=%d", allowedCount, len(scheduled))
	}
}
