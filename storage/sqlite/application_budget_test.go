package sqlite_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestStoreReservesApplicationBudgetIdempotentlyAndReusesReleasedSlot(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	store, applications := applicationBudgetStore(t, now, 2)
	windowStart := now.Truncate(24 * time.Hour)
	params := core.ReserveApplicationBudgetParams{
		ApplicationID: applications[0], ProfileID: "primary", Platform: "hh",
		WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour), Limit: 1, Now: now,
	}
	first, err := store.ReserveApplicationBudget(ctx, params)
	if err != nil {
		t.Fatalf("reserve first slot: %v", err)
	}
	second, err := store.ReserveApplicationBudget(ctx, params)
	if err != nil || second.State != core.ApplicationBudgetReserved || first.ApplicationID != second.ApplicationID {
		t.Fatalf("idempotent reservation=%#v err=%v", second, err)
	}
	params.ApplicationID = applications[1]
	if _, err := store.ReserveApplicationBudget(ctx, params); !core.ErrorIsCategory(err, core.ErrorQuotaExceeded) {
		t.Fatalf("second application error=%v, want quota exceeded", err)
	}
	if err := store.ReleaseApplicationBudget(ctx, applications[0], now.Add(time.Minute)); err != nil {
		t.Fatalf("release first slot: %v", err)
	}
	if _, err := store.ReserveApplicationBudget(ctx, params); err != nil {
		t.Fatalf("reuse released slot: %v", err)
	}
	if err := store.CommitApplicationBudget(ctx, applications[1], now.Add(2*time.Minute)); err != nil {
		t.Fatalf("commit second slot: %v", err)
	}
	if err := store.CommitApplicationBudget(ctx, applications[1], now.Add(3*time.Minute)); err != nil {
		t.Fatalf("idempotent commit: %v", err)
	}
}

func TestStoreApplicationBudgetAllowsOnlyOneConcurrentReservation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	store, applications := applicationBudgetStore(t, now, 2)
	windowStart := now.Truncate(24 * time.Hour)
	start := make(chan struct{})
	results := make(chan error, len(applications))
	var workers sync.WaitGroup
	for _, applicationID := range applications {
		applicationID := applicationID
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := store.ReserveApplicationBudget(ctx, core.ReserveApplicationBudgetParams{
				ApplicationID: applicationID, ProfileID: "primary", Platform: "hh",
				WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour), Limit: 1, Now: now,
			})
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	successes, quotas := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case core.ErrorIsCategory(err, core.ErrorQuotaExceeded):
			quotas++
		default:
			t.Fatalf("unexpected concurrent reservation error: %v", err)
		}
	}
	if successes != 1 || quotas != 1 {
		t.Fatalf("successes=%d quotas=%d", successes, quotas)
	}
}

func applicationBudgetStore(t *testing.T, now time.Time, count int) (interface {
	ReserveApplicationBudget(context.Context, core.ReserveApplicationBudgetParams) (core.ApplicationBudgetReservation, error)
	CommitApplicationBudget(context.Context, core.ApplicationID, time.Time) error
	ReleaseApplicationBudget(context.Context, core.ApplicationID, time.Time) error
	AcquireApplicationPace(context.Context, core.AcquireApplicationPaceParams) (core.ApplicationPaceReservation, bool, error)
}, []core.ApplicationID) {
	t.Helper()
	store, err := openStore(t.TempDir() + "/job-agent.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	applications := make([]core.ApplicationID, 0, count)
	for index := 0; index < count; index++ {
		vacancy := core.Vacancy{
			Platform: "hh", ExternalID: string(rune('1' + index)), Title: "Go",
			State: core.VacancyStateOpen, ObservedAt: now,
		}
		if _, err := store.UpsertVacancy(context.Background(), vacancy); err != nil {
			t.Fatalf("store vacancy: %v", err)
		}
		applicationID := core.ApplicationID("application-" + vacancy.ExternalID)
		application, err := core.NewApplication(applicationID, core.ApplicationKey{ProfileID: "primary", Vacancy: vacancy.Key()}, now)
		if err != nil {
			t.Fatalf("new application: %v", err)
		}
		if _, created, err := store.CreateApplication(context.Background(), application); err != nil || !created {
			t.Fatalf("store application: created=%v err=%v", created, err)
		}
		applications = append(applications, applicationID)
	}
	return store, applications
}

func TestStoreApplicationBudgetReplacesStaleWindowReservation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	store, applications := applicationBudgetStore(t, now, 2)
	windowStart := now.Truncate(24 * time.Hour)
	first := core.ReserveApplicationBudgetParams{
		ApplicationID: applications[0], ProfileID: "primary", Platform: "hh",
		WindowStart: windowStart, WindowEnd: windowStart.Add(24 * time.Hour), Limit: 1, Now: now,
	}
	if _, err := store.ReserveApplicationBudget(ctx, first); err != nil {
		t.Fatalf("reserve first window: %v", err)
	}
	// A crash after the reservation leaves it reserved forever; the next day
	// the retry must reserve in the current window instead of failing.
	next := first
	next.WindowStart = windowStart.Add(24 * time.Hour)
	next.WindowEnd = windowStart.Add(48 * time.Hour)
	next.Now = now.Add(24 * time.Hour)
	reservation, err := store.ReserveApplicationBudget(ctx, next)
	if err != nil {
		t.Fatalf("reserve next window: %v", err)
	}
	if reservation.State != core.ApplicationBudgetReserved || !reservation.WindowStart.Equal(next.WindowStart) {
		t.Fatalf("reservation=%#v", reservation)
	}
	replacement := next
	replacement.ApplicationID = applications[1]
	if _, err := store.ReserveApplicationBudget(ctx, replacement); !core.ErrorIsCategory(err, core.ErrorQuotaExceeded) {
		t.Fatalf("second application error=%v, want quota exceeded", err)
	}
}
