package core

import (
	"errors"
	"strings"
	"time"
)

type ApplicationDisposition string

const (
	ApplicationDispositionPending  ApplicationDisposition = "pending"
	ApplicationDispositionInvited  ApplicationDisposition = "invited"
	ApplicationDispositionRejected ApplicationDisposition = "rejected"
	ApplicationDispositionHidden   ApplicationDisposition = "hidden"
	ApplicationDispositionUnknown  ApplicationDisposition = "unknown"
)

// ApplicationPlatformState is the latest platform-owned state used by the
// operator UI and retention policy. A chat message is deliberately not an
// invitation: only DispositionInvited protects an application from automatic
// cleanup.
type ApplicationPlatformState struct {
	ApplicationID         ApplicationID          `json:"application_id"`
	ExternalNegotiationID string                 `json:"external_negotiation_id"`
	PlatformState         string                 `json:"platform_state"`
	Disposition           ApplicationDisposition `json:"disposition"`
	ViewedByOpponent      *bool                  `json:"viewed_by_opponent,omitempty"`
	PlatformUpdatedAt     *time.Time             `json:"platform_updated_at,omitempty"`
	ObservedAt            time.Time              `json:"observed_at"`
}

func (state ApplicationPlatformState) Validate() error {
	if state.ApplicationID == "" || strings.TrimSpace(state.ExternalNegotiationID) == "" || strings.TrimSpace(state.PlatformState) == "" || state.ObservedAt.IsZero() {
		return errors.New("application platform state requires application, negotiation, state and observation time")
	}
	switch state.Disposition {
	case ApplicationDispositionPending, ApplicationDispositionInvited, ApplicationDispositionRejected,
		ApplicationDispositionHidden, ApplicationDispositionUnknown:
	default:
		return errors.New("application platform state has invalid disposition")
	}
	if state.PlatformUpdatedAt != nil && state.PlatformUpdatedAt.IsZero() {
		return errors.New("application platform state has invalid platform update time")
	}
	return nil
}
