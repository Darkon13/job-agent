package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestProfileMutationLaneSerializesOneProfile(t *testing.T) {
	lane := NewProfileMutationLane()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	results := make(chan error, 2)
	handler := lane.Wrap(func(_ context.Context, task core.Task) error {
		switch task.ID {
		case "first":
			close(firstStarted)
			<-releaseFirst
		case "second":
			close(secondStarted)
		}
		return nil
	})

	go func() { results <- handler(context.Background(), core.Task{ID: "first", ProfileID: "primary"}) }()
	requireSignal(t, firstStarted, "first mutation did not start")
	go func() { results <- handler(context.Background(), core.Task{ID: "second", ProfileID: "primary"}) }()
	select {
	case <-secondStarted:
		t.Fatal("second mutation started before the first one finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	requireSignal(t, secondStarted, "second mutation did not start after release")
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("mutation failed: %v", err)
		}
	}
}

func TestProfileMutationLaneAllowsDifferentProfiles(t *testing.T) {
	lane := NewProfileMutationLane()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	handler := lane.Wrap(func(_ context.Context, task core.Task) error {
		if task.ProfileID == "primary" {
			close(firstStarted)
			<-releaseFirst
			return nil
		}
		close(secondStarted)
		return nil
	})
	results := make(chan error, 2)
	go func() { results <- handler(context.Background(), core.Task{ID: "first", ProfileID: "primary"}) }()
	requireSignal(t, firstStarted, "first profile mutation did not start")
	go func() { results <- handler(context.Background(), core.Task{ID: "second", ProfileID: "secondary"}) }()
	requireSignal(t, secondStarted, "second profile was blocked by another profile")
	close(releaseFirst)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("mutation failed: %v", err)
		}
	}
}

func TestProfileMutationLaneHonorsCancellationWhileWaiting(t *testing.T) {
	lane := NewProfileMutationLane()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- lane.Run(context.Background(), "primary", func() error {
			close(firstStarted)
			<-releaseFirst
			return nil
		})
	}()
	requireSignal(t, firstStarted, "first mutation did not start")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := lane.Run(ctx, "primary", func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("wait result = %v, called = %v; want canceled without mutation", err, called)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first mutation failed: %v", err)
	}
}

func TestProfileMutationLaneRejectsMissingProfile(t *testing.T) {
	err := NewProfileMutationLane().Run(context.Background(), "", func() error { return nil })
	if err == nil {
		t.Fatal("expected missing profile error")
	}
}

func requireSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}
