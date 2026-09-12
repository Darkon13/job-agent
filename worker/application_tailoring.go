package worker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	applicationoperator "github.com/Darkon13/job-agent/operator"
	"github.com/Darkon13/job-agent/storage"
)

type ApplicationTailoringPlan struct {
	Processor       applicationoperator.ResumeTailoringProcessor
	AllowedPaths    []string
	EmployerMatcher *applicationoperator.EmployerGroupMatcher
}

func (plan ApplicationTailoringPlan) Validate() error {
	if plan.Processor == nil || len(plan.AllowedPaths) == 0 {
		return errors.New("application tailoring plan requires processor and allowed paths")
	}
	seen := make(map[string]struct{}, len(plan.AllowedPaths))
	for _, path := range plan.AllowedPaths {
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("application tailoring path %q is not a JSON Pointer", path)
		}
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("application tailoring contains duplicate path %q", path)
		}
		seen[path] = struct{}{}
	}
	return nil
}

func (plan ApplicationTailoringPlan) employerGroups(vacancy core.Vacancy) []string {
	if plan.EmployerMatcher == nil {
		return nil
	}
	matches := plan.EmployerMatcher.Match(vacancy)
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		result = append(result, match.GroupTag)
	}
	return result
}

// ApplicationTailoringCoordinator executes profile mutations inside the
// application worker's profile lane. Every external write is bracketed by a
// persisted saga transition, so a process restart can safely resume apply or
// restore using the same immutable proposal.
type ApplicationTailoringCoordinator struct {
	tailorings storage.ApplicationTailoringRepository
	proposals  storage.ProfileStateProposalRepository
	readers    map[core.ProfileID]adapter.ProfileStateReader
	writers    *ProfileStateWriterRegistry
	clock      Clock
	ids        ApplicationTailoringIDGenerator
}

type ApplicationTailoringIDGenerator interface {
	NewID(prefix string) (string, error)
}

func NewApplicationTailoringCoordinator(tailorings storage.ApplicationTailoringRepository, proposals storage.ProfileStateProposalRepository, readers map[core.ProfileID]adapter.ProfileStateReader, writers *ProfileStateWriterRegistry, clock Clock, ids ApplicationTailoringIDGenerator) (*ApplicationTailoringCoordinator, error) {
	if tailorings == nil || proposals == nil || writers == nil || clock == nil || ids == nil {
		return nil, errors.New("application tailoring coordinator requires repositories, writers, clock and id generator")
	}
	copiedReaders := make(map[core.ProfileID]adapter.ProfileStateReader, len(readers))
	for profileID, reader := range readers {
		if profileID == "" || reader == nil {
			return nil, errors.New("application tailoring readers require profile and implementation")
		}
		copiedReaders[profileID] = reader
	}
	return &ApplicationTailoringCoordinator{
		tailorings: tailorings, proposals: proposals, readers: copiedReaders,
		writers: writers, clock: clock, ids: ids,
	}, nil
}

func (coordinator *ApplicationTailoringCoordinator) Apply(ctx context.Context, application core.Application, vacancy core.Vacancy, plan ApplicationTailoringPlan, resumeID string) (*core.ApplicationTailoring, error) {
	if coordinator == nil {
		return nil, errors.New("application tailoring coordinator is nil")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(resumeID) == "" {
		return nil, errors.New("application tailoring requires resume id")
	}
	tailoring, err := coordinator.tailorings.ApplicationTailoringByApplication(ctx, application.ID)
	if err == nil {
		if tailoring.Attempt != application.Attempts+1 {
			return nil, errors.New("active application tailoring belongs to another submit attempt")
		}
		return coordinator.driveApply(ctx, tailoring)
	}
	if !errors.Is(err, storage.ErrApplicationTailoringNotFound) {
		return nil, err
	}
	reader, err := coordinator.reader(application.Key.ProfileID)
	if err != nil {
		return nil, err
	}
	observation, err := reader.ReadProfileState(ctx, adapter.ProfileStateReadRequest{
		ProfileID: application.Key.ProfileID, Paths: slices.Clone(plan.AllowedPaths),
	})
	if err != nil {
		return nil, err
	}
	input := applicationoperator.ResumeTailoringInput{
		Application: application, Vacancy: vacancy, ResumeID: resumeID,
		EmployerGroups: plan.employerGroups(vacancy), CurrentState: observation,
		AllowedPaths: slices.Clone(plan.AllowedPaths),
	}
	processorPlan, err := plan.Processor.Plan(ctx, input)
	if err != nil {
		if errors.Is(err, applicationoperator.ErrResumeTailoringSkillLimit) {
			// Temporary tailoring is optional: if the skill limit cannot be
			// satisfied, submit on the current resume instead of failing.
			return nil, nil
		}
		return nil, err
	}
	if err := processorPlan.Validate(input); err != nil {
		return nil, err
	}
	if len(processorPlan.Overrides) == 0 {
		return nil, nil
	}
	baseline, err := core.NewProfileStateResource(
		"application-tailoring:"+string(application.ID)+":baseline",
		application.Key.ProfileID, core.ProfileStateOwnershipDeclaredFields, observation.State,
	)
	if err != nil {
		return nil, err
	}
	target, err := baseline.WithOverrides(processorPlan.Overrides)
	if err != nil {
		return nil, err
	}
	id, err := coordinator.ids.NewID("application-tailoring")
	if err != nil {
		return nil, err
	}
	now := coordinator.clock.Now()
	if now.Before(observation.ObservedAt) {
		now = observation.ObservedAt
	}
	candidate, err := core.NewApplicationTailoring(core.NewApplicationTailoringParams{
		ID: core.ApplicationTailoringID(id), ApplicationID: application.ID,
		Attempt: application.Attempts + 1, Key: application.Key, ResumeID: resumeID,
		ProcessorTag: processorPlan.ProcessorTag, ProcessorVersion: processorPlan.ProcessorVersion,
		ProcessorInputDigest: processorPlan.InputDigest, AllowedPaths: plan.AllowedPaths,
		Baseline: observation, TailoredState: target.State,
	}, now)
	if err != nil {
		return nil, err
	}
	stored, _, err := coordinator.tailorings.CreateApplicationTailoring(ctx, candidate)
	if err != nil {
		return nil, err
	}
	return coordinator.driveApply(ctx, stored)
}

func (coordinator *ApplicationTailoringCoordinator) driveApply(ctx context.Context, tailoring core.ApplicationTailoring) (*core.ApplicationTailoring, error) {
	switch tailoring.Status {
	case core.ApplicationTailoringPlanned:
		resource, err := tailoring.TailoredResource()
		if err != nil {
			return nil, err
		}
		observation, err := tailoring.BaselineObservation()
		if err != nil {
			return nil, err
		}
		proposal, err := coordinator.createProposal(ctx, resource, observation)
		if err != nil {
			return nil, err
		}
		expectedRevision := tailoring.Revision
		if err := tailoring.BeginApply(proposal.ID, coordinator.clock.Now()); err != nil {
			return nil, err
		}
		if err := coordinator.tailorings.SaveApplicationTailoring(ctx, tailoring, expectedRevision); err != nil {
			return nil, err
		}
	case core.ApplicationTailoringApplying:
	case core.ApplicationTailoringApplied, core.ApplicationTailoringSubmitting:
		return &tailoring, nil
	case core.ApplicationTailoringRecoveryRequired:
		return nil, coordinator.recoveryError(tailoring)
	default:
		return nil, fmt.Errorf("cannot apply application tailoring in status %q", tailoring.Status)
	}
	proposal, err := coordinator.proposals.ProfileStateProposal(ctx, tailoring.ApplyProposalID)
	if err != nil {
		return nil, err
	}
	result, err := coordinator.applyProposal(ctx, proposal)
	if err != nil {
		return nil, coordinator.handleMutationError(ctx, tailoring, "tailoring apply conflicted with current profile state", err)
	}
	expectedRevision := tailoring.Revision
	if err := tailoring.RecordApplied(result.Observation, coordinator.clock.Now()); err != nil {
		return nil, err
	}
	if err := coordinator.tailorings.SaveApplicationTailoring(ctx, tailoring, expectedRevision); err != nil {
		return nil, err
	}
	return &tailoring, nil
}

func (coordinator *ApplicationTailoringCoordinator) BeginSubmit(ctx context.Context, applicationID core.ApplicationID) error {
	tailoring, err := coordinator.tailorings.ApplicationTailoringByApplication(ctx, applicationID)
	if errors.Is(err, storage.ErrApplicationTailoringNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch tailoring.Status {
	case core.ApplicationTailoringSubmitting:
		return nil
	case core.ApplicationTailoringRecoveryRequired:
		return coordinator.recoveryError(tailoring)
	case core.ApplicationTailoringApplied:
	default:
		return fmt.Errorf("cannot begin submit with application tailoring in status %q", tailoring.Status)
	}
	observation, err := coordinator.read(ctx, tailoring)
	if err != nil {
		return err
	}
	if observation.StateDigest != tailoring.TailoredDigest {
		return coordinator.markRecovery(ctx, tailoring, "profile state drifted after tailoring apply")
	}
	expectedRevision := tailoring.Revision
	if err := tailoring.BeginSubmit(coordinator.clock.Now()); err != nil {
		return err
	}
	return coordinator.tailorings.SaveApplicationTailoring(ctx, tailoring, expectedRevision)
}

func (coordinator *ApplicationTailoringCoordinator) Restore(ctx context.Context, applicationID core.ApplicationID) error {
	tailoring, err := coordinator.tailorings.ApplicationTailoringByApplication(ctx, applicationID)
	if errors.Is(err, storage.ErrApplicationTailoringNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch tailoring.Status {
	case core.ApplicationTailoringRecoveryRequired:
		return coordinator.recoveryError(tailoring)
	case core.ApplicationTailoringApplied, core.ApplicationTailoringSubmitting:
		observation, err := coordinator.read(ctx, tailoring)
		if err != nil {
			return err
		}
		if observation.StateDigest != tailoring.TailoredDigest && observation.StateDigest != tailoring.BaselineDigest {
			return coordinator.markRecovery(ctx, tailoring, "profile state drifted before tailoring restore")
		}
		resource, err := tailoring.BaselineResource()
		if err != nil {
			return err
		}
		proposal, err := coordinator.createProposal(ctx, resource, observation)
		if err != nil {
			return err
		}
		expectedRevision := tailoring.Revision
		if err := tailoring.BeginRestore(proposal.ID, coordinator.clock.Now()); err != nil {
			return err
		}
		if err := coordinator.tailorings.SaveApplicationTailoring(ctx, tailoring, expectedRevision); err != nil {
			return err
		}
	case core.ApplicationTailoringRestoring:
	case core.ApplicationTailoringRestored:
		return nil
	default:
		return fmt.Errorf("cannot restore application tailoring in status %q", tailoring.Status)
	}
	proposal, err := coordinator.proposals.ProfileStateProposal(ctx, tailoring.RestoreProposalID)
	if err != nil {
		return err
	}
	var restored core.ProfileStateObservation
	if proposal.Status == core.ProfileStateProposalNoChanges {
		restored, err = coordinator.read(ctx, tailoring)
	} else {
		result, applyErr := coordinator.applyProposal(ctx, proposal)
		err = applyErr
		restored = result.Observation
	}
	if err != nil {
		return coordinator.handleMutationError(ctx, tailoring, "tailoring restore conflicted with current profile state", err)
	}
	expectedRevision := tailoring.Revision
	if err := tailoring.RecordRestored(restored, coordinator.clock.Now()); err != nil {
		return err
	}
	return coordinator.tailorings.SaveApplicationTailoring(ctx, tailoring, expectedRevision)
}

func (coordinator *ApplicationTailoringCoordinator) createProposal(ctx context.Context, resource core.ProfileStateResource, observation core.ProfileStateObservation) (core.ProfileStateProposal, error) {
	id, err := coordinator.ids.NewID("profile-state-proposal")
	if err != nil {
		return core.ProfileStateProposal{}, err
	}
	proposal, err := core.NewProfileStateProposal(core.ProfileStateProposalID(id), resource, observation, coordinator.clock.Now())
	if err != nil {
		return core.ProfileStateProposal{}, err
	}
	stored, _, err := coordinator.proposals.CreateProfileStateProposal(ctx, proposal)
	return stored, err
}

func (coordinator *ApplicationTailoringCoordinator) applyProposal(ctx context.Context, proposal core.ProfileStateProposal) (adapter.ProfileStateApplyResult, error) {
	writer, err := coordinator.writers.Resolve(proposal.ProfileID)
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	result, err := writer.ApplyProfileState(ctx, proposal)
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	if err := result.Observation.Validate(); err != nil {
		return adapter.ProfileStateApplyResult{}, fmt.Errorf("profile state writer returned invalid observation: %w", err)
	}
	pending, err := proposal.ChangesToApply(result.Observation)
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	if len(pending) != 0 {
		return adapter.ProfileStateApplyResult{}, &core.OperationError{
			Category: core.ErrorAmbiguousResult, Operation: "application.tailoring.apply",
			Message: "profile state writer did not verify every tailoring field",
		}
	}
	return result, nil
}

func (coordinator *ApplicationTailoringCoordinator) read(ctx context.Context, tailoring core.ApplicationTailoring) (core.ProfileStateObservation, error) {
	reader, err := coordinator.reader(tailoring.Key.ProfileID)
	if err != nil {
		return core.ProfileStateObservation{}, err
	}
	return reader.ReadProfileState(ctx, adapter.ProfileStateReadRequest{
		ProfileID: tailoring.Key.ProfileID, Paths: slices.Clone(tailoring.AllowedPaths),
	})
}

func (coordinator *ApplicationTailoringCoordinator) reader(profileID core.ProfileID) (adapter.ProfileStateReader, error) {
	reader := coordinator.readers[profileID]
	if reader == nil {
		return nil, &core.OperationError{
			Category: core.ErrorUnsupported, Operation: "application.tailoring.read",
			Message: "profile has no state reader for application tailoring",
		}
	}
	return reader, nil
}

func (coordinator *ApplicationTailoringCoordinator) handleMutationError(ctx context.Context, tailoring core.ApplicationTailoring, reason string, err error) error {
	if core.ErrorIsCategory(err, core.ErrorConflict) || errors.Is(err, core.ErrProfileStateChanged) {
		if recoveryErr := coordinator.markRecovery(ctx, tailoring, reason); recoveryErr != nil {
			return recoveryErr
		}
	}
	return err
}

func (coordinator *ApplicationTailoringCoordinator) markRecovery(ctx context.Context, tailoring core.ApplicationTailoring, reason string) error {
	expectedRevision := tailoring.Revision
	if err := tailoring.RequireRecovery(reason, coordinator.clock.Now()); err != nil {
		return err
	}
	if err := coordinator.tailorings.SaveApplicationTailoring(ctx, tailoring, expectedRevision); err != nil {
		return err
	}
	return coordinator.recoveryError(tailoring)
}

func (coordinator *ApplicationTailoringCoordinator) recoveryError(tailoring core.ApplicationTailoring) error {
	return &core.OperationError{
		Category: core.ErrorPermanentFailure, Operation: "application.tailoring.recovery",
		Platform: tailoring.Key.Vacancy.Platform,
		Message:  "temporary resume state requires recovery before further applications",
		Metadata: map[string]string{"tailoring_id": string(tailoring.ID)},
	}
}
