package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

type ApplicationSubmitPayload struct {
	ApplicationID ApplicationID  `json:"application_id"`
	Key           ApplicationKey `json:"key"`
}

func (payload ApplicationSubmitPayload) Validate() error {
	if payload.ApplicationID == "" {
		return errors.New("application submit payload requires application id")
	}
	return payload.Key.Validate()
}

// ApplicationSubmitIdempotencyKey is stable across retries and process restarts.
// Hashing avoids delimiter ambiguity in external platform IDs.
func ApplicationSubmitIdempotencyKey(key ApplicationKey) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(string(key.ProfileID) + "\x00" + string(key.Vacancy.Platform) + "\x00" + key.Vacancy.ExternalID))
	return "application.submit:" + hex.EncodeToString(digest[:]), nil
}
