package memory

import (
	"context"
	"errors"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func (repository *Repository) ReserveApplicationBudget(ctx context.Context, params core.ReserveApplicationBudgetParams) (core.ApplicationBudgetReservation, error) {
	if err := ctx.Err(); err != nil {
		return core.ApplicationBudgetReservation{}, err
	}
	if err := params.Validate(); err != nil {
		return core.ApplicationBudgetReservation{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if reservation, exists := repository.applicationBudgets[params.ApplicationID]; exists {
		if reservation.ProfileID != params.ProfileID || reservation.Platform != params.Platform ||
			!reservation.WindowStart.Equal(params.WindowStart) || !reservation.WindowEnd.Equal(params.WindowEnd) {
			return core.ApplicationBudgetReservation{}, errors.New("application budget reservation conflicts with another window")
		}
		if reservation.State != core.ApplicationBudgetReleased {
			return reservation, nil
		}
	}
	used := 0
	for _, reservation := range repository.applicationBudgets {
		if reservation.ProfileID == params.ProfileID && reservation.Platform == params.Platform &&
			reservation.WindowStart.Equal(params.WindowStart) && reservation.State != core.ApplicationBudgetReleased {
			used++
		}
	}
	if used >= params.Limit {
		resetAt := params.WindowEnd
		return core.ApplicationBudgetReservation{}, &core.OperationError{
			Category: core.ErrorQuotaExceeded, Operation: "applications.budget.reserve", Platform: params.Platform,
			RetryAfter: &resetAt, Message: "daily application budget is exhausted",
		}
	}
	reservation := core.ApplicationBudgetReservation{
		ApplicationID: params.ApplicationID, ProfileID: params.ProfileID, Platform: params.Platform,
		WindowStart: params.WindowStart, WindowEnd: params.WindowEnd, Limit: params.Limit,
		State: core.ApplicationBudgetReserved, CreatedAt: params.Now, UpdatedAt: params.Now,
	}
	repository.applicationBudgets[params.ApplicationID] = reservation
	return reservation, nil
}

func (repository *Repository) CommitApplicationBudget(ctx context.Context, applicationID core.ApplicationID, now time.Time) error {
	return repository.finishApplicationBudget(ctx, applicationID, core.ApplicationBudgetCommitted, now)
}

func (repository *Repository) ReleaseApplicationBudget(ctx context.Context, applicationID core.ApplicationID, now time.Time) error {
	return repository.finishApplicationBudget(ctx, applicationID, core.ApplicationBudgetReleased, now)
}

func (repository *Repository) finishApplicationBudget(ctx context.Context, applicationID core.ApplicationID, target core.ApplicationBudgetState, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if applicationID == "" || now.IsZero() {
		return errors.New("application budget update requires application and current time")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	reservation, exists := repository.applicationBudgets[applicationID]
	if !exists {
		if target == core.ApplicationBudgetReleased {
			return nil
		}
		return errors.New("application budget reservation not found")
	}
	if now.Before(reservation.UpdatedAt) {
		return errors.New("application budget update time must not move backwards")
	}
	if reservation.State == target {
		return nil
	}
	if reservation.State != core.ApplicationBudgetReserved {
		return errors.New("application budget reservation is already final")
	}
	reservation.State = target
	reservation.UpdatedAt = now
	repository.applicationBudgets[applicationID] = reservation
	return nil
}

func (repository *Repository) ApplicationBudget(applicationID core.ApplicationID) (core.ApplicationBudgetReservation, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	reservation, exists := repository.applicationBudgets[applicationID]
	return reservation, exists
}
