package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	ProfileStateOwnershipDeclaredFields = "declared_fields"
	maximumProfileStateBytes            = 256 << 10
	maximumProfileStateChanges          = 1024
	maximumProfileStateDepth            = 32
)

type ProfileStateProposalStatus string

const (
	ProfileStateProposalPlanned   ProfileStateProposalStatus = "planned"
	ProfileStateProposalNoChanges ProfileStateProposalStatus = "no_changes"
)

// ProfileStateResource is a canonical, platform-neutral desired-state
// manifest. State contains only resolved values; processor references must be
// evaluated before constructing this resource.
type ProfileStateResource struct {
	Tag            string          `json:"tag"`
	ProfileID      ProfileID       `json:"profile_id"`
	Ownership      string          `json:"ownership"`
	State          json.RawMessage `json:"state"`
	ManifestDigest string          `json:"manifest_digest"`
}

func NewProfileStateResource(tag string, profileID ProfileID, ownership string, state json.RawMessage) (ProfileStateResource, error) {
	tag = strings.TrimSpace(tag)
	ownership = strings.TrimSpace(ownership)
	canonical, leaves, err := canonicalProfileState(state)
	if err != nil {
		return ProfileStateResource{}, err
	}
	if leaves == 0 {
		return ProfileStateResource{}, errors.New("profile state resource must declare at least one field")
	}
	resource := ProfileStateResource{Tag: tag, ProfileID: profileID, Ownership: ownership, State: canonical}
	resource.ManifestDigest = profileStateManifestDigest(resource)
	if err := resource.Validate(); err != nil {
		return ProfileStateResource{}, err
	}
	return resource, nil
}

func (resource ProfileStateResource) Validate() error {
	if strings.TrimSpace(resource.Tag) == "" || resource.ProfileID == "" {
		return errors.New("profile state resource requires tag and profile id")
	}
	if resource.Ownership != ProfileStateOwnershipDeclaredFields {
		return fmt.Errorf("profile state resource ownership must be %q", ProfileStateOwnershipDeclaredFields)
	}
	canonical, leaves, err := canonicalProfileState(resource.State)
	if err != nil {
		return err
	}
	if leaves == 0 {
		return errors.New("profile state resource must declare at least one field")
	}
	if !bytes.Equal(canonical, resource.State) {
		return errors.New("profile state resource must contain canonical JSON")
	}
	if resource.ManifestDigest == "" || resource.ManifestDigest != profileStateManifestDigest(resource) {
		return errors.New("profile state resource manifest digest does not match its contents")
	}
	return nil
}

// DeclaredPaths returns the leaf fields owned by this resource as sorted JSON
// Pointers. It exposes the read contract without exposing desired values.
func (resource ProfileStateResource) DeclaredPaths() ([]string, error) {
	if err := resource.Validate(); err != nil {
		return nil, err
	}
	value, err := decodeJSONValue(resource.State)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	collectDeclaredPaths("", value, &paths)
	sort.Strings(paths)
	return paths, nil
}

type ProfileStateObservation struct {
	ProfileID      ProfileID       `json:"profile_id"`
	State          json.RawMessage `json:"state"`
	StateDigest    string          `json:"state_digest"`
	RemoteRevision string          `json:"remote_revision,omitempty"`
	ObservedAt     time.Time       `json:"observed_at"`
}

func NewProfileStateObservation(profileID ProfileID, state json.RawMessage, remoteRevision string, observedAt time.Time) (ProfileStateObservation, error) {
	canonical, _, err := canonicalProfileState(state)
	if err != nil {
		return ProfileStateObservation{}, err
	}
	observation := ProfileStateObservation{
		ProfileID: profileID, State: canonical, StateDigest: profileStateDigest(canonical),
		RemoteRevision: strings.TrimSpace(remoteRevision), ObservedAt: observedAt,
	}
	if err := observation.Validate(); err != nil {
		return ProfileStateObservation{}, err
	}
	return observation, nil
}

func (observation ProfileStateObservation) Validate() error {
	if observation.ProfileID == "" || observation.ObservedAt.IsZero() {
		return errors.New("profile state observation requires profile id and observed time")
	}
	canonical, _, err := canonicalProfileState(observation.State)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, observation.State) {
		return errors.New("profile state observation must contain canonical JSON")
	}
	if observation.StateDigest == "" || observation.StateDigest != profileStateDigest(observation.State) {
		return errors.New("profile state observation digest does not match its contents")
	}
	return nil
}

type ProfileStateChange struct {
	Path          string `json:"path"`
	Operation     string `json:"operation"`
	BeforePresent bool   `json:"before_present"`
	BeforeDigest  string `json:"before_digest,omitempty"`
	AfterDigest   string `json:"after_digest"`
}

type ProfileStateProposal struct {
	ID              ProfileStateProposalID     `json:"id"`
	ResourceTag     string                     `json:"resource_tag"`
	ProfileID       ProfileID                  `json:"profile_id"`
	Ownership       string                     `json:"ownership"`
	Status          ProfileStateProposalStatus `json:"status"`
	IdempotencyKey  string                     `json:"idempotency_key"`
	ManifestDigest  string                     `json:"manifest_digest"`
	ObservedDigest  string                     `json:"observed_digest"`
	DesiredDigest   string                     `json:"desired_digest"`
	RemoteRevision  string                     `json:"remote_revision,omitempty"`
	DesiredState    json.RawMessage            `json:"-"`
	Changes         []ProfileStateChange       `json:"changes"`
	ObservationTime time.Time                  `json:"observation_time"`
	Revision        uint64                     `json:"revision"`
	CreatedAt       time.Time                  `json:"created_at"`
	UpdatedAt       time.Time                  `json:"updated_at"`
}

func NewProfileStateProposal(id ProfileStateProposalID, resource ProfileStateResource, observation ProfileStateObservation, now time.Time) (ProfileStateProposal, error) {
	if id == "" || now.IsZero() {
		return ProfileStateProposal{}, errors.New("profile state proposal requires id and current time")
	}
	if err := resource.Validate(); err != nil {
		return ProfileStateProposal{}, fmt.Errorf("profile state proposal resource: %w", err)
	}
	if err := observation.Validate(); err != nil {
		return ProfileStateProposal{}, fmt.Errorf("profile state proposal observation: %w", err)
	}
	if resource.ProfileID != observation.ProfileID {
		return ProfileStateProposal{}, errors.New("profile state proposal resource and observation profiles differ")
	}
	changes, err := diffProfileState(observation.State, resource.State)
	if err != nil {
		return ProfileStateProposal{}, err
	}
	status := ProfileStateProposalPlanned
	if len(changes) == 0 {
		status = ProfileStateProposalNoChanges
	}
	proposal := ProfileStateProposal{
		ID: id, ResourceTag: resource.Tag, ProfileID: resource.ProfileID, Ownership: resource.Ownership,
		Status: status, ManifestDigest: resource.ManifestDigest, ObservedDigest: observation.StateDigest,
		DesiredDigest: profileStateDigest(resource.State), RemoteRevision: observation.RemoteRevision,
		DesiredState: append(json.RawMessage(nil), resource.State...), Changes: changes,
		ObservationTime: observation.ObservedAt, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	proposal.IdempotencyKey = profileStateProposalKey(proposal)
	if err := proposal.Validate(); err != nil {
		return ProfileStateProposal{}, err
	}
	return proposal, nil
}

func (proposal ProfileStateProposal) Validate() error {
	if proposal.ID == "" || strings.TrimSpace(proposal.ResourceTag) == "" || proposal.ProfileID == "" {
		return errors.New("profile state proposal requires id, resource tag and profile id")
	}
	if proposal.Ownership != ProfileStateOwnershipDeclaredFields {
		return fmt.Errorf("profile state proposal ownership must be %q", ProfileStateOwnershipDeclaredFields)
	}
	if proposal.ManifestDigest == "" || proposal.ObservedDigest == "" || proposal.DesiredDigest == "" {
		return errors.New("profile state proposal requires manifest, observed and desired digests")
	}
	canonical, leaves, err := canonicalProfileState(proposal.DesiredState)
	if err != nil || leaves == 0 || !bytes.Equal(canonical, proposal.DesiredState) {
		return errors.New("profile state proposal requires canonical desired state")
	}
	if proposal.DesiredDigest != profileStateDigest(proposal.DesiredState) {
		return errors.New("profile state proposal desired digest does not match its contents")
	}
	if len(proposal.Changes) > maximumProfileStateChanges {
		return fmt.Errorf("profile state proposal exceeds %d changes", maximumProfileStateChanges)
	}
	if proposal.Status == ProfileStateProposalNoChanges && len(proposal.Changes) != 0 || proposal.Status == ProfileStateProposalPlanned && len(proposal.Changes) == 0 {
		return errors.New("profile state proposal status does not match its changes")
	}
	if proposal.Status != ProfileStateProposalPlanned && proposal.Status != ProfileStateProposalNoChanges {
		return fmt.Errorf("unsupported profile state proposal status %q", proposal.Status)
	}
	previous := ""
	for _, change := range proposal.Changes {
		if change.Path == "" || change.Path <= previous || change.Operation != "set" && change.Operation != "clear" || change.AfterDigest == "" {
			return errors.New("profile state proposal contains invalid or unsorted changes")
		}
		if change.BeforePresent != (change.BeforeDigest != "") {
			return errors.New("profile state proposal change presence does not match before digest")
		}
		previous = change.Path
	}
	if proposal.ObservationTime.IsZero() || proposal.Revision != 1 || proposal.CreatedAt.IsZero() || !proposal.UpdatedAt.Equal(proposal.CreatedAt) {
		return errors.New("profile state proposal requires immutable revision-one timestamps")
	}
	if proposal.IdempotencyKey == "" || proposal.IdempotencyKey != profileStateProposalKey(proposal) {
		return errors.New("profile state proposal idempotency key does not match its inputs")
	}
	return nil
}

func diffProfileState(observedRaw, desiredRaw json.RawMessage) ([]ProfileStateChange, error) {
	observed, err := decodeJSONValue(observedRaw)
	if err != nil {
		return nil, err
	}
	desired, err := decodeJSONValue(desiredRaw)
	if err != nil {
		return nil, err
	}
	changes := make([]ProfileStateChange, 0)
	diffDeclaredValue("", observed, true, desired, &changes)
	if len(changes) > maximumProfileStateChanges {
		return nil, fmt.Errorf("profile state diff exceeds %d changes", maximumProfileStateChanges)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func diffDeclaredValue(path string, observed any, observedPresent bool, desired any, changes *[]ProfileStateChange) {
	if desiredObject, ok := desired.(map[string]any); ok {
		observedObject, _ := observed.(map[string]any)
		keys := make([]string, 0, len(desiredObject))
		for key := range desiredObject {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			current, exists := observedObject[key]
			diffDeclaredValue(path+"/"+escapeJSONPointer(key), current, exists, desiredObject[key], changes)
		}
		return
	}
	if observedPresent && jsonValuesEqual(observed, desired) {
		return
	}
	operation := "set"
	if desired == nil {
		operation = "clear"
		if !observedPresent || observed == nil {
			return
		}
	}
	change := ProfileStateChange{Path: path, Operation: operation, BeforePresent: observedPresent, AfterDigest: digestJSONValue(desired)}
	if observedPresent {
		change.BeforeDigest = digestJSONValue(observed)
	}
	*changes = append(*changes, change)
}

func collectDeclaredPaths(path string, value any, paths *[]string) {
	if object, ok := value.(map[string]any); ok {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectDeclaredPaths(path+"/"+escapeJSONPointer(key), object[key], paths)
		}
		return
	}
	*paths = append(*paths, path)
}

func canonicalProfileState(raw json.RawMessage) (json.RawMessage, int, error) {
	if len(raw) == 0 || len(raw) > maximumProfileStateBytes {
		return nil, 0, fmt.Errorf("profile state must contain between 1 and %d bytes", maximumProfileStateBytes)
	}
	decoded, err := decodeJSONValue(raw)
	if err != nil {
		return nil, 0, fmt.Errorf("decode profile state: %w", err)
	}
	root, ok := decoded.(map[string]any)
	if !ok {
		return nil, 0, errors.New("profile state must be a JSON object")
	}
	for key := range root {
		if key != "profile" && key != "resumes" {
			return nil, 0, fmt.Errorf("profile state contains unsupported root field %q", key)
		}
	}
	leaves := 0
	if value, exists := root["profile"]; exists {
		profile, ok := value.(map[string]any)
		if !ok {
			return nil, 0, errors.New("profile state profile must be an object")
		}
		count, err := validateAndCountObject(profile, "profile", 1)
		if err != nil {
			return nil, 0, err
		}
		leaves += count
	}
	if value, exists := root["resumes"]; exists {
		resumes, ok := value.(map[string]any)
		if !ok {
			return nil, 0, errors.New("profile state resumes must be an object")
		}
		for resumeID, value := range resumes {
			if strings.TrimSpace(resumeID) == "" {
				return nil, 0, errors.New("profile state contains an empty resume id")
			}
			resume, ok := value.(map[string]any)
			if !ok {
				return nil, 0, fmt.Errorf("profile state resume %q must be an object", resumeID)
			}
			count, err := validateAndCountObject(resume, "resume "+resumeID, 1)
			if err != nil {
				return nil, 0, err
			}
			leaves += count
		}
	}
	canonical, err := json.Marshal(root)
	if err != nil {
		return nil, 0, fmt.Errorf("encode canonical profile state: %w", err)
	}
	return canonical, leaves, nil
}

func validateAndCountObject(value map[string]any, path string, depth int) (int, error) {
	if depth > maximumProfileStateDepth {
		return 0, fmt.Errorf("profile state %s exceeds maximum nesting depth %d", path, maximumProfileStateDepth)
	}
	count := 0
	for key, child := range value {
		if strings.TrimSpace(key) == "" {
			return 0, fmt.Errorf("profile state %s contains an empty field name", path)
		}
		if key == "processor" {
			return 0, fmt.Errorf("profile state %s contains an unresolved processor reference", path)
		}
		if object, ok := child.(map[string]any); ok {
			childCount, err := validateAndCountObject(object, path+"."+key, depth+1)
			if err != nil {
				return 0, err
			}
			count += childCount
		} else {
			if err := validateNestedProfileStateValue(child, path+"."+key, depth+1); err != nil {
				return 0, err
			}
			count++
		}
	}
	return count, nil
}

func validateNestedProfileStateValue(value any, path string, depth int) error {
	if depth > maximumProfileStateDepth {
		return fmt.Errorf("profile state %s exceeds maximum nesting depth %d", path, maximumProfileStateDepth)
	}
	switch nested := value.(type) {
	case map[string]any:
		_, err := validateAndCountObject(nested, path, depth)
		return err
	case []any:
		for index, child := range nested {
			if err := validateNestedProfileStateValue(child, fmt.Sprintf("%s[%d]", path, index), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeJSONValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON contains trailing data")
	}
	return value, nil
}

func digestJSONValue(value any) string {
	encoded, _ := json.Marshal(value)
	return profileStateDigest(encoded)
}

func jsonValuesEqual(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func escapeJSONPointer(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

func profileStateDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func profileStateManifestDigest(resource ProfileStateResource) string {
	return profileStateDigest([]byte(resource.Tag + "\x00" + string(resource.ProfileID) + "\x00" + resource.Ownership + "\x00" + string(resource.State)))
}

func profileStateProposalKey(proposal ProfileStateProposal) string {
	value := proposal.ResourceTag + "\x00" + string(proposal.ProfileID) + "\x00" + proposal.ManifestDigest + "\x00" + proposal.ObservedDigest + "\x00" + proposal.RemoteRevision
	digest := sha256.Sum256([]byte(value))
	return "profile-state-plan:" + hex.EncodeToString(digest[:])
}
