package core

import (
	"errors"
	"strings"
	"time"
)

type ApplicationRemovalReason string

const (
	ApplicationRemovalManual            ApplicationRemovalReason = "manual"
	ApplicationRemovalRetentionRejected ApplicationRemovalReason = "retention_rejected"
	ApplicationRemovalRetentionStale    ApplicationRemovalReason = "retention_stale"
	// ApplicationRemovalRetentionValidation removes a questionnaire or test
	// application that was never submitted; no platform state exists for it.
	ApplicationRemovalRetentionValidation ApplicationRemovalReason = "retention_validation"
)

type ApplicationTombstone struct {
	ApplicationID ApplicationID            `json:"application_id"`
	Key           ApplicationKey           `json:"key"`
	Reason        ApplicationRemovalReason `json:"reason"`
	RemovedAt     time.Time                `json:"removed_at"`
	Status        ApplicationStatus        `json:"status"`
}

// ApplicationRemoval carries the exact policy and observation checked by GC.
// Repositories recheck it under their write lock/transaction before deleting.
type ApplicationRemoval struct {
	Reason      ApplicationRemovalReason
	StaleBefore time.Time
	ObservedAt  time.Time
}

func (request ApplicationRemoval) Eligible(application Application, state ApplicationPlatformState, now time.Time) bool {
	if request.Reason == ApplicationRemovalManual {
		return true
	}
	if request.Reason == ApplicationRemovalRetentionValidation {
		return application.Status == ApplicationWaitingValidation || application.Status == ApplicationSkipped
	}
	if application.Status != ApplicationSubmitted || state.ApplicationID != application.ID ||
		!state.ObservedAt.Equal(request.ObservedAt) || !FreshApplicationObservation(state.ObservedAt, now) {
		return false
	}
	switch request.Reason {
	case ApplicationRemovalRetentionRejected:
		return state.Disposition == ApplicationDispositionRejected
	case ApplicationRemovalRetentionStale:
		return state.Disposition == ApplicationDispositionPending && !request.StaleBefore.IsZero() &&
			application.SubmittedAt != nil && !application.SubmittedAt.After(request.StaleBefore)
	}
	return false
}

func FreshApplicationObservation(observedAt, now time.Time) bool {
	return !observedAt.IsZero() && !observedAt.After(now) && now.Sub(observedAt) <= 5*time.Minute
}

func (reason ApplicationRemovalReason) Validate() error {
	switch ApplicationRemovalReason(strings.TrimSpace(string(reason))) {
	case ApplicationRemovalManual, ApplicationRemovalRetentionRejected, ApplicationRemovalRetentionStale,
		ApplicationRemovalRetentionValidation:
		return nil
	default:
		return errors.New("application removal has invalid reason")
	}
}

func (tombstone ApplicationTombstone) Validate() error {
	if tombstone.ApplicationID == "" || tombstone.RemovedAt.IsZero() {
		return errors.New("application tombstone requires application and removal time")
	}
	if err := tombstone.Key.Validate(); err != nil {
		return err
	}
	return tombstone.Reason.Validate()
}
