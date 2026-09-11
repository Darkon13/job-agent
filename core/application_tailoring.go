package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

type ApplicationTailoringStatus string

const (
	ApplicationTailoringPlanned          ApplicationTailoringStatus = "planned"
	ApplicationTailoringApplying         ApplicationTailoringStatus = "applying"
	ApplicationTailoringApplied          ApplicationTailoringStatus = "applied"
	ApplicationTailoringSubmitting       ApplicationTailoringStatus = "submitting"
	ApplicationTailoringRestoring        ApplicationTailoringStatus = "restoring"
	ApplicationTailoringRestored         ApplicationTailoringStatus = "restored"
	ApplicationTailoringRecoveryRequired ApplicationTailoringStatus = "recovery_required"
)

// ApplicationTailoring owns one temporary profile mutation. BaselineState and
// TailoredState are private snapshots: public JSON contains only digests,
// changed paths and processor provenance.
type ApplicationTailoring struct {
	ID                     ApplicationTailoringID     `json:"id"`
	ApplicationID          ApplicationID              `json:"application_id"`
	Attempt                int                        `json:"attempt"`
	Key                    ApplicationKey             `json:"key"`
	ResumeID               string                     `json:"resume_id"`
	Status                 ApplicationTailoringStatus `json:"status"`
	IdempotencyKey         string                     `json:"idempotency_key"`
	ProcessorTag           string                     `json:"processor_tag"`
	ProcessorVersion       string                     `json:"processor_version"`
	ProcessorInputDigest   string                     `json:"processor_input_digest"`
	AllowedPaths           []string                   `json:"allowed_paths"`
	BaselineDigest         string                     `json:"baseline_digest"`
	TailoredDigest         string                     `json:"tailored_digest"`
	BaselineRemoteRevision string                     `json:"baseline_remote_revision,omitempty"`
	BaselineObservedAt     time.Time                  `json:"baseline_observed_at"`
	TailoredRemoteRevision string                     `json:"tailored_remote_revision,omitempty"`
	BaselineState          json.RawMessage            `json:"-"`
	TailoredState          json.RawMessage            `json:"-"`
	ApplyProposalID        ProfileStateProposalID     `json:"apply_proposal_id,omitempty"`
	RestoreProposalID      ProfileStateProposalID     `json:"restore_proposal_id,omitempty"`
	RecoveryReason         string                     `json:"recovery_reason,omitempty"`
	Revision               uint64                     `json:"revision"`
	CreatedAt              time.Time                  `json:"created_at"`
	UpdatedAt              time.Time                  `json:"updated_at"`
}

type NewApplicationTailoringParams struct {
	ID                   ApplicationTailoringID
	ApplicationID        ApplicationID
	Attempt              int
	Key                  ApplicationKey
	ResumeID             string
	ProcessorTag         string
	ProcessorVersion     string
	ProcessorInputDigest string
	AllowedPaths         []string
	Baseline             ProfileStateObservation
	TailoredState        json.RawMessage
}

func NewApplicationTailoring(params NewApplicationTailoringParams, now time.Time) (ApplicationTailoring, error) {
	baselineState, baselinePaths, err := canonicalProfileState(params.Baseline.State)
	if err != nil {
		return ApplicationTailoring{}, fmt.Errorf("application tailoring baseline: %w", err)
	}
	tailoredState, tailoredPaths, err := canonicalProfileState(params.TailoredState)
	if err != nil {
		return ApplicationTailoring{}, fmt.Errorf("application tailoring target: %w", err)
	}
	if baselinePaths == 0 || tailoredPaths == 0 {
		return ApplicationTailoring{}, errors.New("application tailoring requires non-empty baseline and target states")
	}
	tailoring := ApplicationTailoring{
		ID: params.ID, ApplicationID: params.ApplicationID, Attempt: params.Attempt, Key: params.Key,
		ResumeID: strings.TrimSpace(params.ResumeID), Status: ApplicationTailoringPlanned,
		ProcessorTag: strings.TrimSpace(params.ProcessorTag), ProcessorVersion: strings.TrimSpace(params.ProcessorVersion),
		ProcessorInputDigest: params.ProcessorInputDigest, AllowedPaths: slices.Clone(params.AllowedPaths),
		BaselineDigest: params.Baseline.StateDigest, TailoredDigest: profileStateDigest(tailoredState),
		BaselineRemoteRevision: params.Baseline.RemoteRevision,
		BaselineObservedAt:     params.Baseline.ObservedAt,
		BaselineState:          baselineState, TailoredState: tailoredState,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	sort.Strings(tailoring.AllowedPaths)
	tailoring.IdempotencyKey = applicationTailoringKey(tailoring)
	if err := tailoring.Validate(); err != nil {
		return ApplicationTailoring{}, err
	}
	if params.Baseline.ProfileID != params.Key.ProfileID {
		return ApplicationTailoring{}, errors.New("application tailoring baseline belongs to another profile")
	}
	if tailoring.BaselineDigest == tailoring.TailoredDigest {
		return ApplicationTailoring{}, errors.New("application tailoring requires at least one effective change")
	}
	return tailoring, nil
}

func (tailoring ApplicationTailoring) Validate() error {
	if tailoring.ID == "" || tailoring.ApplicationID == "" || tailoring.Attempt < 1 || strings.TrimSpace(tailoring.ResumeID) == "" {
		return errors.New("application tailoring requires id, application attempt and resume")
	}
	if err := tailoring.Key.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(tailoring.ProcessorTag) == "" || strings.TrimSpace(tailoring.ProcessorVersion) == "" ||
		!validApplicationDigest(tailoring.ProcessorInputDigest) {
		return errors.New("application tailoring requires processor provenance")
	}
	baseline, baselineLeaves, err := canonicalProfileState(tailoring.BaselineState)
	if err != nil || baselineLeaves == 0 || !bytes.Equal(baseline, tailoring.BaselineState) {
		return errors.New("application tailoring requires canonical baseline state")
	}
	tailored, tailoredLeaves, err := canonicalProfileState(tailoring.TailoredState)
	if err != nil || tailoredLeaves == 0 || !bytes.Equal(tailored, tailoring.TailoredState) {
		return errors.New("application tailoring requires canonical tailored state")
	}
	if tailoring.BaselineDigest != profileStateDigest(tailoring.BaselineState) || tailoring.TailoredDigest != profileStateDigest(tailoring.TailoredState) ||
		tailoring.BaselineDigest == tailoring.TailoredDigest {
		return errors.New("application tailoring state digests do not match snapshots")
	}
	if tailoring.BaselineObservedAt.IsZero() || tailoring.BaselineObservedAt.After(tailoring.CreatedAt) {
		return errors.New("application tailoring requires a baseline observation no later than creation")
	}
	if err := validateApplicationTailoringPaths(tailoring.BaselineState, tailoring.TailoredState, tailoring.AllowedPaths); err != nil {
		return err
	}
	switch tailoring.Status {
	case ApplicationTailoringPlanned:
		if tailoring.ApplyProposalID != "" || tailoring.RestoreProposalID != "" || tailoring.TailoredRemoteRevision != "" {
			return errors.New("planned application tailoring cannot contain execution results")
		}
	case ApplicationTailoringApplying:
		if tailoring.ApplyProposalID == "" || tailoring.RestoreProposalID != "" || tailoring.TailoredRemoteRevision != "" {
			return errors.New("applying application tailoring requires only apply proposal")
		}
	case ApplicationTailoringApplied, ApplicationTailoringSubmitting:
		if tailoring.ApplyProposalID == "" || tailoring.RestoreProposalID != "" {
			return errors.New("applied application tailoring requires apply proposal and no restore proposal")
		}
	case ApplicationTailoringRestoring:
		if tailoring.ApplyProposalID == "" || tailoring.RestoreProposalID == "" {
			return errors.New("restoring application tailoring requires apply and restore proposals")
		}
	case ApplicationTailoringRestored:
		if tailoring.ApplyProposalID == "" || tailoring.RestoreProposalID == "" || tailoring.RecoveryReason != "" {
			return errors.New("restored application tailoring requires proposals and no recovery reason")
		}
	case ApplicationTailoringRecoveryRequired:
		if strings.TrimSpace(tailoring.RecoveryReason) == "" {
			return errors.New("application tailoring recovery requires a reason")
		}
	default:
		return fmt.Errorf("unknown application tailoring status %q", tailoring.Status)
	}
	if tailoring.Status != ApplicationTailoringRecoveryRequired && tailoring.RecoveryReason != "" {
		return errors.New("only recovery-required application tailoring may contain a recovery reason")
	}
	if tailoring.Revision == 0 || tailoring.CreatedAt.IsZero() || tailoring.UpdatedAt.IsZero() || tailoring.UpdatedAt.Before(tailoring.CreatedAt) {
		return errors.New("application tailoring requires revision and valid timestamps")
	}
	if tailoring.IdempotencyKey == "" || tailoring.IdempotencyKey != applicationTailoringKey(tailoring) {
		return errors.New("application tailoring idempotency key does not match immutable inputs")
	}
	return nil
}

func (tailoring *ApplicationTailoring) BeginApply(proposalID ProfileStateProposalID, now time.Time) error {
	if tailoring == nil || tailoring.Status != ApplicationTailoringPlanned || proposalID == "" {
		return errors.New("only planned application tailoring can begin apply")
	}
	if err := tailoring.advance(now); err != nil {
		return err
	}
	tailoring.ApplyProposalID = proposalID
	tailoring.Status = ApplicationTailoringApplying
	return tailoring.Validate()
}

func (tailoring *ApplicationTailoring) RecordApplied(observation ProfileStateObservation, now time.Time) error {
	if tailoring == nil || tailoring.Status != ApplicationTailoringApplying {
		return errors.New("only applying application tailoring can record apply")
	}
	if observation.ProfileID != tailoring.Key.ProfileID || observation.StateDigest != tailoring.TailoredDigest {
		return errors.New("application tailoring apply read-back does not match tailored state")
	}
	if err := tailoring.advance(now); err != nil {
		return err
	}
	tailoring.TailoredRemoteRevision = observation.RemoteRevision
	tailoring.Status = ApplicationTailoringApplied
	return tailoring.Validate()
}

func (tailoring *ApplicationTailoring) BeginSubmit(now time.Time) error {
	if tailoring == nil || tailoring.Status != ApplicationTailoringApplied {
		return errors.New("only applied application tailoring can begin submit")
	}
	if err := tailoring.advance(now); err != nil {
		return err
	}
	tailoring.Status = ApplicationTailoringSubmitting
	return tailoring.Validate()
}

func (tailoring *ApplicationTailoring) BeginRestore(proposalID ProfileStateProposalID, now time.Time) error {
	if tailoring == nil || (tailoring.Status != ApplicationTailoringApplied && tailoring.Status != ApplicationTailoringSubmitting) || proposalID == "" {
		return errors.New("only applied or submitting application tailoring can begin restore")
	}
	if err := tailoring.advance(now); err != nil {
		return err
	}
	tailoring.RestoreProposalID = proposalID
	tailoring.Status = ApplicationTailoringRestoring
	return tailoring.Validate()
}

func (tailoring *ApplicationTailoring) RecordRestored(observation ProfileStateObservation, now time.Time) error {
	if tailoring == nil || tailoring.Status != ApplicationTailoringRestoring {
		return errors.New("only restoring application tailoring can record restore")
	}
	if observation.ProfileID != tailoring.Key.ProfileID || observation.StateDigest != tailoring.BaselineDigest {
		return errors.New("application tailoring restore read-back does not match baseline state")
	}
	if err := tailoring.advance(now); err != nil {
		return err
	}
	tailoring.Status = ApplicationTailoringRestored
	return tailoring.Validate()
}

func (tailoring *ApplicationTailoring) RequireRecovery(reason string, now time.Time) error {
	if tailoring == nil || tailoring.Status == ApplicationTailoringRestored {
		return errors.New("restored application tailoring cannot require recovery")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("application tailoring recovery requires a reason")
	}
	if err := tailoring.advance(now); err != nil {
		return err
	}
	tailoring.Status = ApplicationTailoringRecoveryRequired
	tailoring.RecoveryReason = reason
	return tailoring.Validate()
}

func (tailoring ApplicationTailoring) TailoredResource() (ProfileStateResource, error) {
	return NewProfileStateResource("application-tailoring:"+string(tailoring.ID)+":apply", tailoring.Key.ProfileID, ProfileStateOwnershipDeclaredFields, tailoring.TailoredState)
}

func (tailoring ApplicationTailoring) BaselineResource() (ProfileStateResource, error) {
	return NewProfileStateResource("application-tailoring:"+string(tailoring.ID)+":restore", tailoring.Key.ProfileID, ProfileStateOwnershipDeclaredFields, tailoring.BaselineState)
}

func (tailoring ApplicationTailoring) BaselineObservation() (ProfileStateObservation, error) {
	return NewProfileStateObservation(
		tailoring.Key.ProfileID, tailoring.BaselineState,
		tailoring.BaselineRemoteRevision, tailoring.BaselineObservedAt,
	)
}

func (tailoring *ApplicationTailoring) advance(now time.Time) error {
	if now.IsZero() || now.Before(tailoring.UpdatedAt) {
		return errors.New("application tailoring update time must not move backwards")
	}
	tailoring.Revision++
	tailoring.UpdatedAt = now
	return nil
}

func validateApplicationTailoringPaths(baselineRaw, tailoredRaw json.RawMessage, paths []string) error {
	if len(paths) == 0 {
		return errors.New("application tailoring requires allowed paths")
	}
	baseline, _ := decodeJSONValue(baselineRaw)
	tailored, _ := decodeJSONValue(tailoredRaw)
	seen := make(map[string]struct{}, len(paths))
	previous := ""
	changed := 0
	for _, path := range paths {
		if !strings.HasPrefix(path, "/") || path <= previous {
			return errors.New("application tailoring allowed paths must be sorted unique JSON Pointers")
		}
		if _, duplicate := seen[path]; duplicate {
			return errors.New("application tailoring allowed paths must be unique")
		}
		seen[path] = struct{}{}
		previous = path
		before, beforeExists := jsonPointerValue(baseline, path)
		after, afterExists := jsonPointerValue(tailored, path)
		if !beforeExists || !afterExists {
			return fmt.Errorf("application tailoring path %q must exist in baseline and target", path)
		}
		if !jsonValuesEqual(before, after) {
			changed++
		}
	}
	baselinePaths := make([]string, 0)
	collectDeclaredPaths("", baseline, &baselinePaths)
	tailoredPaths := make([]string, 0)
	collectDeclaredPaths("", tailored, &tailoredPaths)
	sort.Strings(baselinePaths)
	sort.Strings(tailoredPaths)
	if !slices.Equal(paths, baselinePaths) || !slices.Equal(paths, tailoredPaths) {
		return errors.New("application tailoring snapshots must contain exactly the allowed paths")
	}
	if changed == 0 {
		return errors.New("application tailoring snapshots contain no changed path")
	}
	return nil
}

func applicationTailoringKey(tailoring ApplicationTailoring) string {
	hash := sha256.New()
	for _, value := range []string{
		string(tailoring.ApplicationID), fmt.Sprintf("%d", tailoring.Attempt), string(tailoring.Key.ProfileID), tailoring.Key.Vacancy.String(), tailoring.ResumeID,
		tailoring.ProcessorTag, tailoring.ProcessorVersion, tailoring.ProcessorInputDigest,
		tailoring.BaselineDigest, tailoring.TailoredDigest, tailoring.BaselineObservedAt.UTC().Format(time.RFC3339Nano),
		strings.Join(tailoring.AllowedPaths, "\x00"),
	} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return "application.tailoring:" + hex.EncodeToString(hash.Sum(nil))
}
