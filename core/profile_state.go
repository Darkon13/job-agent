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

var ErrProfileStateChanged = errors.New("profile state changed since proposal")

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

// ProfileStateValueOverride changes one already-declared leaf for a one-shot
// proposal. It never mutates the registered resource or expands its ownership.
type ProfileStateValueOverride struct {
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
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

// ValueAt returns one declared desired value as canonical JSON.
func (resource ProfileStateResource) ValueAt(path string) (json.RawMessage, bool, error) {
	if err := resource.Validate(); err != nil {
		return nil, false, err
	}
	paths, err := resource.DeclaredPaths()
	if err != nil {
		return nil, false, err
	}
	index := sort.SearchStrings(paths, path)
	if index == len(paths) || paths[index] != path {
		return nil, false, nil
	}
	value, err := decodeJSONValue(resource.State)
	if err != nil {
		return nil, false, err
	}
	selected, exists := jsonPointerValue(value, path)
	if !exists {
		return nil, false, nil
	}
	encoded, err := json.Marshal(selected)
	if err != nil {
		return nil, false, fmt.Errorf("encode profile state value at %s: %w", path, err)
	}
	return encoded, true, nil
}

// WithOverrides derives an immutable one-shot resource from a registered
// source resource. Overrides may replace existing leaves only; they cannot add
// fields or silently broaden declared ownership.
func (resource ProfileStateResource) WithOverrides(overrides []ProfileStateValueOverride) (ProfileStateResource, error) {
	if err := resource.Validate(); err != nil {
		return ProfileStateResource{}, err
	}
	if len(overrides) == 0 {
		return ProfileStateResource{}, errors.New("profile state override requires at least one value")
	}
	declaredPaths, err := resource.DeclaredPaths()
	if err != nil {
		return ProfileStateResource{}, err
	}
	declared := make(map[string]struct{}, len(declaredPaths))
	for _, path := range declaredPaths {
		declared[path] = struct{}{}
	}
	state, err := decodeJSONValue(resource.State)
	if err != nil {
		return ProfileStateResource{}, err
	}
	seen := make(map[string]struct{}, len(overrides))
	for _, override := range overrides {
		if _, exists := declared[override.Path]; !exists {
			return ProfileStateResource{}, fmt.Errorf("profile state override path %q is not a declared leaf", override.Path)
		}
		if _, duplicate := seen[override.Path]; duplicate {
			return ProfileStateResource{}, fmt.Errorf("duplicate profile state override path %q", override.Path)
		}
		seen[override.Path] = struct{}{}
		value, err := decodeJSONValue(override.Value)
		if err != nil {
			return ProfileStateResource{}, fmt.Errorf("decode profile state override %q: %w", override.Path, err)
		}
		if _, object := value.(map[string]any); object {
			return ProfileStateResource{}, fmt.Errorf("profile state override %q cannot replace a leaf with an object", override.Path)
		}
		if !setJSONPointerValue(state, override.Path, value) {
			return ProfileStateResource{}, fmt.Errorf("profile state override path %q cannot be replaced", override.Path)
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return ProfileStateResource{}, fmt.Errorf("encode overridden profile state: %w", err)
	}
	derived, err := NewProfileStateResource(resource.Tag, resource.ProfileID, resource.Ownership, encoded)
	if err != nil {
		return ProfileStateResource{}, err
	}
	derivedPaths, err := derived.DeclaredPaths()
	if err != nil {
		return ProfileStateResource{}, err
	}
	if len(derivedPaths) != len(declaredPaths) {
		return ProfileStateResource{}, errors.New("profile state override changed declared ownership")
	}
	for index := range declaredPaths {
		if derivedPaths[index] != declaredPaths[index] {
			return ProfileStateResource{}, errors.New("profile state override changed declared ownership")
		}
	}
	return derived, nil
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

// ValueAt returns one observed value as canonical JSON. A missing path is
// reported separately from an explicit JSON null so callers can preserve the
// exact pre-mutation state when building compensating workflows.
func (observation ProfileStateObservation) ValueAt(path string) (json.RawMessage, bool, error) {
	if err := observation.Validate(); err != nil {
		return nil, false, err
	}
	if !strings.HasPrefix(path, "/") {
		return nil, false, fmt.Errorf("profile state path %q is not a JSON Pointer", path)
	}
	state, err := decodeJSONValue(observation.State)
	if err != nil {
		return nil, false, err
	}
	value, exists := jsonPointerValue(state, path)
	if !exists {
		return nil, false, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false, fmt.Errorf("encode observed profile state value at %s: %w", path, err)
	}
	return encoded, true, nil
}

// PathsEmpty reports whether every requested field is safe for an initial
// bootstrap. Missing fields, nulls, empty strings and empty containers count as
// empty. Scalar zero values are deliberately treated as populated because they
// may be meaningful platform values.
func (observation ProfileStateObservation) PathsEmpty(paths []string) (bool, error) {
	if err := observation.Validate(); err != nil {
		return false, err
	}
	if len(paths) == 0 {
		return false, errors.New("profile state empty check requires at least one path")
	}
	state, err := decodeJSONValue(observation.State)
	if err != nil {
		return false, err
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, "/") {
			return false, fmt.Errorf("profile state path %q is not a JSON Pointer", path)
		}
		value, exists := jsonPointerValue(state, path)
		if exists && !profileStateValueEmpty(value) {
			return false, nil
		}
	}
	return true, nil
}

func profileStateValueEmpty(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case string:
		return value == ""
	case []any:
		return len(value) == 0
	case map[string]any:
		return len(value) == 0
	default:
		return false
	}
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

// DeclaredPaths returns every field owned by the immutable desired snapshot.
func (proposal ProfileStateProposal) DeclaredPaths() ([]string, error) {
	if err := proposal.Validate(); err != nil {
		return nil, err
	}
	value, err := decodeJSONValue(proposal.DesiredState)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	collectDeclaredPaths("", value, &paths)
	sort.Strings(paths)
	return paths, nil
}

// ChangesToApply classifies each declared field against the proposal's
// before/after digests. This makes a retry after a partially completed apply
// safe while rejecting any third state written after the plan was created.
func (proposal ProfileStateProposal) ChangesToApply(observation ProfileStateObservation) ([]ProfileStateChange, error) {
	if err := proposal.Validate(); err != nil {
		return nil, err
	}
	if err := observation.Validate(); err != nil {
		return nil, err
	}
	if proposal.ProfileID != observation.ProfileID {
		return nil, errors.New("profile state proposal and observation profiles differ")
	}
	desired, err := decodeJSONValue(proposal.DesiredState)
	if err != nil {
		return nil, err
	}
	observed, err := decodeJSONValue(observation.State)
	if err != nil {
		return nil, err
	}
	changes := make(map[string]ProfileStateChange, len(proposal.Changes))
	for _, change := range proposal.Changes {
		changes[change.Path] = change
	}
	paths, err := proposal.DeclaredPaths()
	if err != nil {
		return nil, err
	}
	pending := make([]ProfileStateChange, 0, len(proposal.Changes))
	for _, path := range paths {
		desiredValue, desiredPresent := jsonPointerValue(desired, path)
		if !desiredPresent {
			return nil, errors.New("profile state proposal declared path is missing from desired state")
		}
		observedValue, observedPresent := jsonPointerValue(observed, path)
		change, changedAtPlan := changes[path]
		if !changedAtPlan {
			if !profileStateValueMatches(observedValue, observedPresent, desiredValue, true) {
				return nil, fmt.Errorf("%w at %s", ErrProfileStateChanged, path)
			}
			continue
		}
		if profileStateValueMatches(observedValue, observedPresent, desiredValue, true) {
			continue
		}
		beforeMatches := observedPresent == change.BeforePresent
		if observedPresent {
			beforeMatches = beforeMatches && digestJSONValue(observedValue) == change.BeforeDigest
		}
		if !beforeMatches {
			return nil, fmt.Errorf("%w at %s", ErrProfileStateChanged, path)
		}
		pending = append(pending, change)
	}
	return pending, nil
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

func profileStateValueMatches(observed any, observedPresent bool, desired any, desiredPresent bool) bool {
	if desiredPresent && desired == nil && !observedPresent {
		return true
	}
	return observedPresent == desiredPresent && observedPresent && jsonValuesEqual(observed, desired)
}

func jsonPointerValue(root any, pointer string) (any, bool) {
	if pointer == "" {
		return root, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := root
	for _, rawSegment := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		segment, ok := unescapeJSONPointer(rawSegment)
		if !ok {
			return nil, false
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func setJSONPointerValue(root any, pointer string, value any) bool {
	if !strings.HasPrefix(pointer, "/") {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	if len(segments) == 0 {
		return false
	}
	current := root
	for index, rawSegment := range segments {
		segment, ok := unescapeJSONPointer(rawSegment)
		if !ok {
			return false
		}
		object, ok := current.(map[string]any)
		if !ok {
			return false
		}
		if index == len(segments)-1 {
			if _, exists := object[segment]; !exists {
				return false
			}
			object[segment] = value
			return true
		}
		current, ok = object[segment]
		if !ok {
			return false
		}
	}
	return false
}

func unescapeJSONPointer(value string) (string, bool) {
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '~' {
			result.WriteByte(value[index])
			continue
		}
		if index+1 >= len(value) {
			return "", false
		}
		index++
		switch value[index] {
		case '0':
			result.WriteByte('~')
		case '1':
			result.WriteByte('/')
		default:
			return "", false
		}
	}
	return result.String(), true
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
