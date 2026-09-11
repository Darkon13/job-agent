package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	applicationoperator "github.com/Darkon13/job-agent/operator"
	"github.com/Darkon13/job-agent/storage"
)

var (
	ErrApplicationTailoringNoChanges        = errors.New("application tailoring produced no changes")
	ErrApplicationTailoringRecoveryRequired = errors.New("application tailoring requires recovery before profile can be used")
)

type ApplicationTailoringRequest struct {
	Application    core.Application
	Vacancy        core.Vacancy
	ResumeID       string
	EmployerGroups []string
	AllowedPaths   []string
	Processor      applicationoperator.ResumeTailoringProcessor
	Reader         adapter.ProfileStateReader
}

type ApplicationTailoringStartResult struct {
	Tailoring core.ApplicationTailoring
	Proposal  core.ProfileStateProposal
	ApplyTask core.Task
	Created   bool
}

// ApplicationTailoringWorkflow performs only the durable plan/apply handoff.
// The platform writer remains in profile_state.apply; later orchestration can
// observe that task and advance submit/restore without duplicating mutation
// transports here.
type ApplicationTailoringWorkflow struct {
	tailorings storage.ApplicationTailoringRepository
	states     *ProfileStatePlanner
	apply      *ProfileStateApplyWorkflow
	clock      Clock
	ids        IDGenerator
}

func NewApplicationTailoringWorkflow(tailorings storage.ApplicationTailoringRepository, states *ProfileStatePlanner, apply *ProfileStateApplyWorkflow, clock Clock, ids IDGenerator) (*ApplicationTailoringWorkflow, error) {
	if tailorings == nil || states == nil || apply == nil || clock == nil || ids == nil {
		return nil, errors.New("application tailoring workflow requires repositories, profile state workflows, clock and id generator")
	}
	return &ApplicationTailoringWorkflow{tailorings: tailorings, states: states, apply: apply, clock: clock, ids: ids}, nil
}

func (workflow *ApplicationTailoringWorkflow) PlanAndEnqueue(ctx context.Context, request ApplicationTailoringRequest) (ApplicationTailoringStartResult, error) {
	if workflow == nil {
		return ApplicationTailoringStartResult{}, errors.New("application tailoring workflow is nil")
	}
	if request.Application.ID == "" {
		return ApplicationTailoringStartResult{}, errors.New("application tailoring request requires application")
	}
	existing, err := workflow.tailorings.ApplicationTailoringByApplication(ctx, request.Application.ID)
	if err == nil {
		return workflow.ensureApply(ctx, existing, false)
	}
	if !errors.Is(err, storage.ErrApplicationTailoringNotFound) {
		return ApplicationTailoringStartResult{}, err
	}
	if request.Reader == nil || request.Processor == nil {
		return ApplicationTailoringStartResult{}, errors.New("application tailoring request requires reader and processor")
	}
	if strings.TrimSpace(request.ResumeID) == "" || len(request.AllowedPaths) == 0 {
		return ApplicationTailoringStartResult{}, errors.New("application tailoring request requires resume and allowed paths")
	}
	observation, err := request.Reader.ReadProfileState(ctx, adapter.ProfileStateReadRequest{
		ProfileID: request.Application.Key.ProfileID, Paths: append([]string(nil), request.AllowedPaths...),
	})
	if err != nil {
		return ApplicationTailoringStartResult{}, err
	}
	input := applicationoperator.ResumeTailoringInput{
		Application: request.Application, Vacancy: request.Vacancy, ResumeID: request.ResumeID,
		EmployerGroups: append([]string(nil), request.EmployerGroups...), CurrentState: observation,
		AllowedPaths: append([]string(nil), request.AllowedPaths...),
	}
	plan, err := request.Processor.Plan(ctx, input)
	if err != nil {
		return ApplicationTailoringStartResult{}, err
	}
	if err := plan.Validate(input); err != nil {
		return ApplicationTailoringStartResult{}, err
	}
	if len(plan.Overrides) == 0 {
		return ApplicationTailoringStartResult{}, ErrApplicationTailoringNoChanges
	}
	baseline, err := core.NewProfileStateResource(
		"application-tailoring:"+string(request.Application.ID)+":baseline",
		request.Application.Key.ProfileID, core.ProfileStateOwnershipDeclaredFields, observation.State,
	)
	if err != nil {
		return ApplicationTailoringStartResult{}, err
	}
	tailored, err := baseline.WithOverrides(plan.Overrides)
	if err != nil {
		return ApplicationTailoringStartResult{}, err
	}
	id, err := workflow.ids.NewID("application-tailoring")
	if err != nil {
		return ApplicationTailoringStartResult{}, err
	}
	candidate, err := core.NewApplicationTailoring(core.NewApplicationTailoringParams{
		ID: core.ApplicationTailoringID(id), ApplicationID: request.Application.ID,
		Attempt: request.Application.Attempts + 1, Key: request.Application.Key,
		ResumeID: request.ResumeID, ProcessorTag: plan.ProcessorTag, ProcessorVersion: plan.ProcessorVersion,
		ProcessorInputDigest: plan.InputDigest, AllowedPaths: request.AllowedPaths,
		Baseline: observation, TailoredState: tailored.State,
	}, workflow.clock.Now())
	if err != nil {
		return ApplicationTailoringStartResult{}, err
	}
	stored, created, err := workflow.tailorings.CreateApplicationTailoring(ctx, candidate)
	if err != nil {
		return ApplicationTailoringStartResult{}, err
	}
	return workflow.ensureApply(ctx, stored, created)
}

func (workflow *ApplicationTailoringWorkflow) ensureApply(ctx context.Context, tailoring core.ApplicationTailoring, created bool) (ApplicationTailoringStartResult, error) {
	result := ApplicationTailoringStartResult{Tailoring: tailoring, Created: created}
	switch tailoring.Status {
	case core.ApplicationTailoringRecoveryRequired:
		return result, fmt.Errorf("%w: %s", ErrApplicationTailoringRecoveryRequired, tailoring.RecoveryReason)
	case core.ApplicationTailoringRestored, core.ApplicationTailoringApplied,
		core.ApplicationTailoringSubmitting, core.ApplicationTailoringRestoring:
		return result, nil
	case core.ApplicationTailoringPlanned:
		observation, err := tailoring.BaselineObservation()
		if err != nil {
			return result, err
		}
		resource, err := tailoring.TailoredResource()
		if err != nil {
			return result, err
		}
		proposal, _, err := workflow.states.planResource(ctx, resource, observation)
		if err != nil {
			return result, err
		}
		if proposal.Status != core.ProfileStateProposalPlanned {
			return result, errors.New("application tailoring apply proposal unexpectedly contains no changes")
		}
		expectedRevision := tailoring.Revision
		if err := tailoring.BeginApply(proposal.ID, workflow.clock.Now()); err != nil {
			return result, err
		}
		if err := workflow.tailorings.SaveApplicationTailoring(ctx, tailoring, expectedRevision); err != nil {
			return result, err
		}
		result.Tailoring = tailoring
		result.Proposal = proposal
	case core.ApplicationTailoringApplying:
		proposal, err := workflow.states.repository.ProfileStateProposal(ctx, tailoring.ApplyProposalID)
		if err != nil {
			return result, err
		}
		result.Proposal = proposal
	default:
		return result, fmt.Errorf("cannot ensure application tailoring apply in status %q", tailoring.Status)
	}
	task, taskCreated, err := workflow.apply.Enqueue(ctx, result.Tailoring.ApplyProposalID, "application-tailoring:"+string(result.Tailoring.ID))
	if err != nil {
		return result, err
	}
	result.ApplyTask = task
	result.Created = result.Created || taskCreated
	return result, nil
}
