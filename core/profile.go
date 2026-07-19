package core

import (
	"errors"
	"time"
)

type ProfileStatus string

const (
	ProfileEnabled      ProfileStatus = "enabled"
	ProfileDisabled     ProfileStatus = "disabled"
	ProfileAuthRequired ProfileStatus = "auth_required"
)

// Profile is an account on one platform, not a browser process or container.
type Profile struct {
	ID                ProfileID         `json:"id"`
	AdapterInstanceID AdapterInstanceID `json:"adapter_instance_id"`
	Platform          Platform          `json:"platform"`
	ExternalAccountID string            `json:"external_account_id,omitempty"`
	Status            ProfileStatus     `json:"status"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

func NewProfile(id ProfileID, adapterID AdapterInstanceID, platform Platform, now time.Time) (Profile, error) {
	if id == "" || adapterID == "" || platform == "" {
		return Profile{}, errors.New("profile requires id, adapter instance id and platform")
	}
	if now.IsZero() {
		return Profile{}, errors.New("profile requires current time")
	}
	return Profile{
		ID: id, AdapterInstanceID: adapterID, Platform: platform,
		Status: ProfileEnabled, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (profile *Profile) SetStatus(status ProfileStatus, now time.Time) error {
	if profile == nil {
		return errors.New("profile is nil")
	}
	switch status {
	case ProfileEnabled, ProfileDisabled, ProfileAuthRequired:
	default:
		return errors.New("invalid profile status")
	}
	if now.IsZero() || now.Before(profile.UpdatedAt) {
		return errors.New("profile status time must not move backwards")
	}
	profile.Status = status
	profile.UpdatedAt = now
	return nil
}
