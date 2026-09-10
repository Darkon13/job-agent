package memory

import (
	"context"
	"errors"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func (repository *Repository) AcquireApplicationPace(ctx context.Context, params core.AcquireApplicationPaceParams) (core.ApplicationPaceReservation, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.ApplicationPaceReservation{}, false, err
	}
	if err := params.Validate(); err != nil {
		return core.ApplicationPaceReservation{}, false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()

	reservation, exists := repository.applicationPacing[params.ApplicationID]
	if exists && (reservation.ProfileID != params.ProfileID || reservation.Platform != params.Platform) {
		return core.ApplicationPaceReservation{}, false, errors.New("application pacing reservation conflicts with another profile or platform")
	}
	if !exists {
		reservation = core.ApplicationPaceReservation{
			ApplicationID: params.ApplicationID, ProfileID: params.ProfileID, Platform: params.Platform,
			ScheduledAt: params.Now, Interval: params.Interval,
		}
		reservation.ScheduledAt = repository.applicationPacingTail(reservation, params.Now)
	}
	if reservation.ScheduledAt.After(params.Now) {
		repository.applicationPacing[params.ApplicationID] = reservation
		return cloneApplicationPaceReservation(reservation), false, nil
	}

	nextAllowed := repository.applicationPacingNextAllowed(reservation)
	if nextAllowed.After(params.Now) {
		reservation.ScheduledAt = repository.applicationPacingTail(reservation, nextAllowed)
		reservation.AcquiredAt = nil
		repository.applicationPacing[params.ApplicationID] = reservation
		return cloneApplicationPaceReservation(reservation), false, nil
	}
	acquiredAt := params.Now
	reservation.AcquiredAt = &acquiredAt
	repository.applicationPacing[params.ApplicationID] = reservation
	return cloneApplicationPaceReservation(reservation), true, nil
}

func (repository *Repository) applicationPacingTail(current core.ApplicationPaceReservation, floor time.Time) time.Time {
	tail := floor
	for applicationID, reservation := range repository.applicationPacing {
		if applicationID == current.ApplicationID || reservation.ProfileID != current.ProfileID || reservation.Platform != current.Platform {
			continue
		}
		candidate := reservation.ScheduledAt.Add(reservation.Interval)
		if candidate.After(tail) {
			tail = candidate
		}
	}
	return tail
}

func (repository *Repository) applicationPacingNextAllowed(current core.ApplicationPaceReservation) time.Time {
	var next time.Time
	for applicationID, reservation := range repository.applicationPacing {
		if applicationID == current.ApplicationID || reservation.ProfileID != current.ProfileID || reservation.Platform != current.Platform || reservation.AcquiredAt == nil {
			continue
		}
		candidate := reservation.AcquiredAt.Add(reservation.Interval)
		if candidate.After(next) {
			next = candidate
		}
	}
	if current.AcquiredAt != nil {
		candidate := current.AcquiredAt.Add(current.Interval)
		if candidate.After(next) {
			next = candidate
		}
	}
	return next
}

func cloneApplicationPaceReservation(reservation core.ApplicationPaceReservation) core.ApplicationPaceReservation {
	if reservation.AcquiredAt != nil {
		value := *reservation.AcquiredAt
		reservation.AcquiredAt = &value
	}
	return reservation
}
