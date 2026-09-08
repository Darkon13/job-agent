package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	ProfileBootstrapAPIVersion = "job-agent/v1"
	ProfileBootstrapKind       = "ProfileBootstrap"
)

// ProfileBootstrapManifest is a portable, one-shot desired-state document.
// State remains platform-owned; core only validates its bounded JSON shape and
// turns it into the same immutable resource used by scheduled reconciliation.
type ProfileBootstrapManifest struct {
	APIVersion string                   `json:"api_version"`
	Kind       string                   `json:"kind"`
	Metadata   ProfileBootstrapMetadata `json:"metadata"`
	Spec       ProfileBootstrapSpec     `json:"spec"`
}

type ProfileBootstrapMetadata struct {
	Name string `json:"name"`
}

type ProfileBootstrapSpec struct {
	ProfileID ProfileID       `json:"profile_id"`
	Ownership string          `json:"ownership,omitempty"`
	State     json.RawMessage `json:"state"`
}

// DecodeProfileBootstrapManifest rejects misspelled control fields while
// deliberately leaving the adapter-owned state object extensible.
func DecodeProfileBootstrapManifest(data []byte) (ProfileBootstrapManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest ProfileBootstrapManifest
	if err := decoder.Decode(&manifest); err != nil {
		return ProfileBootstrapManifest{}, fmt.Errorf("decode profile bootstrap manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return ProfileBootstrapManifest{}, errors.New("profile bootstrap manifest must contain one JSON value")
		}
		return ProfileBootstrapManifest{}, fmt.Errorf("decode profile bootstrap trailing data: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return ProfileBootstrapManifest{}, err
	}
	return manifest, nil
}

func (manifest ProfileBootstrapManifest) Validate() error {
	if manifest.APIVersion != ProfileBootstrapAPIVersion {
		return fmt.Errorf("profile bootstrap api_version must be %q", ProfileBootstrapAPIVersion)
	}
	if manifest.Kind != ProfileBootstrapKind {
		return fmt.Errorf("profile bootstrap kind must be %q", ProfileBootstrapKind)
	}
	if strings.TrimSpace(manifest.Metadata.Name) == "" {
		return errors.New("profile bootstrap metadata.name is required")
	}
	if manifest.Spec.ProfileID == "" {
		return errors.New("profile bootstrap spec.profile_id is required")
	}
	_, err := manifest.Resource()
	return err
}

func (manifest ProfileBootstrapManifest) Resource() (ProfileStateResource, error) {
	ownership := strings.TrimSpace(manifest.Spec.Ownership)
	if ownership == "" {
		ownership = ProfileStateOwnershipDeclaredFields
	}
	return NewProfileStateResource(
		strings.TrimSpace(manifest.Metadata.Name), manifest.Spec.ProfileID, ownership, manifest.Spec.State,
	)
}
