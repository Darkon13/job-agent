package worker

import (
	"context"
	"errors"
	"sync"

	"github.com/Darkon13/job-agent/core"
)

// ProfileMutationLane serializes state-changing operations for one profile
// while allowing different profiles to make progress independently.
//
// The lane is process-local by design: the current runtime has exactly one
// long-lived backend service. A deployment with multiple backend replicas must
// replace it with a shared lease before enabling mutating workers in more than
// one replica.
type ProfileMutationLane struct {
	mu    sync.Mutex
	lanes map[core.ProfileID]chan struct{}
}

func NewProfileMutationLane() *ProfileMutationLane {
	return &ProfileMutationLane{lanes: make(map[core.ProfileID]chan struct{})}
}

func (lane *ProfileMutationLane) Wrap(next HandlerFunc) HandlerFunc {
	return func(ctx context.Context, task core.Task) error {
		if next == nil {
			return errors.New("profile mutation lane requires handler")
		}
		return lane.Run(ctx, task.ProfileID, func() error {
			return next(ctx, task)
		})
	}
}

func (lane *ProfileMutationLane) Run(ctx context.Context, profileID core.ProfileID, mutate func() error) error {
	if lane == nil || mutate == nil {
		return errors.New("profile mutation lane requires lane and operation")
	}
	if profileID == "" {
		return errors.New("profile mutation lane requires profile")
	}
	semaphore := lane.semaphore(profileID)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-semaphore:
	}
	defer func() { semaphore <- struct{}{} }()
	return mutate()
}

func (lane *ProfileMutationLane) semaphore(profileID core.ProfileID) chan struct{} {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	semaphore := lane.lanes[profileID]
	if semaphore == nil {
		semaphore = make(chan struct{}, 1)
		semaphore <- struct{}{}
		lane.lanes[profileID] = semaphore
	}
	return semaphore
}
