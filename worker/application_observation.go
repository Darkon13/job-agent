package worker

import (
	"errors"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

func validateApplicationObservation(result adapter.ApplicationStateObservationResult, now time.Time) error {
	if !core.FreshApplicationObservation(result.ObservedAt, now) {
		return errors.New("application observation is stale or has an invalid timestamp")
	}
	seen := make(map[string]bool)
	for _, item := range result.Applications {
		state := observationState("validation", item, result.ObservedAt)
		if item.ExternalVacancyID == "" || seen[item.ExternalNegotiationID] {
			return errors.New("application observation has missing or duplicate identity")
		}
		if err := state.Validate(); err != nil {
			return err
		}
		seen[item.ExternalNegotiationID] = true
	}
	return nil
}

func observationState(id core.ApplicationID, item adapter.ApplicationStateObservation, observedAt time.Time) core.ApplicationPlatformState {
	return core.ApplicationPlatformState{ApplicationID: id, ExternalNegotiationID: item.ExternalNegotiationID,
		PlatformState: item.PlatformState, Disposition: item.Disposition, ViewedByOpponent: item.ViewedByOpponent,
		PlatformUpdatedAt: item.PlatformUpdatedAt, ObservedAt: observedAt}
}

func matchApplicationObservation(application core.Application, result adapter.ApplicationStateObservationResult) (core.ApplicationPlatformState, bool) {
	var match core.ApplicationPlatformState
	count := 0
	for _, item := range result.Applications {
		if item.ExternalVacancyID != application.Key.Vacancy.ExternalID {
			continue
		}
		if application.ExternalNegotiationID != "" && application.ExternalNegotiationID != item.ExternalNegotiationID {
			continue
		}
		count++
		match = observationState(application.ID, item, result.ObservedAt)
	}
	// A vacancy can have multiple negotiations for different resumes. Never
	// pick the last one; an unknown association cannot authorize deletion.
	return match, count == 1
}
