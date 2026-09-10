package core

import (
	"errors"
	"time"
)

// ApplicationPaceReservation assigns one durable submission slot to an
// application. AcquiredAt is set only when the worker may perform the external
// submit; ScheduledAt may move after downtime to preserve spacing.
type ApplicationPaceReservation struct {
	ApplicationID ApplicationID
	ProfileID     ProfileID
	Platform      Platform
	ScheduledAt   time.Time
	Interval      time.Duration
	AcquiredAt    *time.Time
}

type AcquireApplicationPaceParams struct {
	ApplicationID ApplicationID
	ProfileID     ProfileID
	Platform      Platform
	Interval      time.Duration
	Now           time.Time
}

func (params AcquireApplicationPaceParams) Validate() error {
	if params.ApplicationID == "" || params.ProfileID == "" || params.Platform == "" {
		return errors.New("application pacing requires application, profile and platform")
	}
	if params.Interval <= 0 {
		return errors.New("application pacing interval must be positive")
	}
	if params.Now.IsZero() {
		return errors.New("application pacing requires current time")
	}
	return nil
}
