package core

import (
	"errors"
	"time"
)

type ApplicationExecutionMode string

const (
	ApplicationExecutionDryRun   ApplicationExecutionMode = "dry_run"
	ApplicationExecutionApproval ApplicationExecutionMode = "approval"
	ApplicationExecutionSubmit   ApplicationExecutionMode = "submit"
)

func (mode ApplicationExecutionMode) Validate() error {
	switch mode {
	case ApplicationExecutionDryRun, ApplicationExecutionApproval, ApplicationExecutionSubmit:
		return nil
	default:
		return errors.New("unknown application execution mode")
	}
}

type ApplicationBudgetState string

const (
	ApplicationBudgetReserved  ApplicationBudgetState = "reserved"
	ApplicationBudgetCommitted ApplicationBudgetState = "committed"
	ApplicationBudgetReleased  ApplicationBudgetState = "released"
)

type ApplicationBudgetReservation struct {
	ApplicationID ApplicationID
	ProfileID     ProfileID
	Platform      Platform
	WindowStart   time.Time
	WindowEnd     time.Time
	Limit         int
	State         ApplicationBudgetState
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ApplicationBudgetUsage reports how much of the profile's daily budget the
// current window has reserved. Found is false when the window has no bucket
// yet, which means nothing was planned today.
type ApplicationBudgetUsage struct {
	ProfileID   ProfileID
	Platform    Platform
	WindowStart time.Time
	WindowEnd   time.Time
	Limit       int
	Used        int
	Found       bool
}

// Exhausted reports that the window already reserved its whole limit. An
// unconfigured limit (zero) is never exhausted.
func (usage ApplicationBudgetUsage) Exhausted() bool {
	return usage.Found && usage.Limit > 0 && usage.Used >= usage.Limit
}

type ReserveApplicationBudgetParams struct {
	ApplicationID ApplicationID
	ProfileID     ProfileID
	Platform      Platform
	WindowStart   time.Time
	WindowEnd     time.Time
	Limit         int
	Now           time.Time
}

func (params ReserveApplicationBudgetParams) Validate() error {
	if params.ApplicationID == "" || params.ProfileID == "" || params.Platform == "" {
		return errors.New("application budget requires application, profile and platform")
	}
	if params.WindowStart.IsZero() || params.WindowEnd.IsZero() || !params.WindowEnd.After(params.WindowStart) {
		return errors.New("application budget requires a valid time window")
	}
	if params.Limit < 1 {
		return errors.New("application budget limit must be positive")
	}
	if params.Now.IsZero() || params.Now.Before(params.WindowStart) || !params.Now.Before(params.WindowEnd) {
		return errors.New("application budget time must belong to its window")
	}
	return nil
}
