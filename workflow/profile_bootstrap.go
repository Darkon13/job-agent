package workflow

import (
	"context"
	"errors"
	"strings"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

// ProfileBootstrapResult distinguishes a condition miss from an idempotent
// plan/task reuse without exposing desired profile values.
type ProfileBootstrapResult struct {
	ConditionMatched bool
	Proposal         core.ProfileStateProposal
	ProposalCreated  bool
	Task             core.Task
	TaskCreated      bool
}

// ProfileBootstrapWorkflow performs the initial read and condition check, then
// delegates mutation to the regular durable profile_state.apply workflow.
type ProfileBootstrapWorkflow struct {
	planner *ProfileStatePlanner
	apply   *ProfileStateApplyWorkflow
}

func NewProfileBootstrapWorkflow(planner *ProfileStatePlanner, apply *ProfileStateApplyWorkflow) (*ProfileBootstrapWorkflow, error) {
	if planner == nil || apply == nil {
		return nil, errors.New("profile bootstrap workflow requires planner and apply workflow")
	}
	return &ProfileBootstrapWorkflow{planner: planner, apply: apply}, nil
}

// RunWhenEmpty applies the manifest only if every field owned by it is absent
// or empty in one trusted platform observation. A partially populated profile
// is skipped as a whole rather than partially overwritten.
func (workflow *ProfileBootstrapWorkflow) RunWhenEmpty(ctx context.Context, resource core.ProfileStateResource, reader adapter.ProfileStateReader, source string) (ProfileBootstrapResult, error) {
	if workflow == nil || reader == nil {
		return ProfileBootstrapResult{}, errors.New("profile bootstrap requires workflow and reader")
	}
	if strings.TrimSpace(source) == "" {
		return ProfileBootstrapResult{}, errors.New("profile bootstrap requires source")
	}
	if err := resource.Validate(); err != nil {
		return ProfileBootstrapResult{}, err
	}
	if !workflow.apply.Writable(resource.ProfileID) {
		return ProfileBootstrapResult{}, ErrProfileStateWriterUnavailable
	}
	paths, err := resource.DeclaredPaths()
	if err != nil {
		return ProfileBootstrapResult{}, err
	}
	observation, err := reader.ReadProfileState(ctx, adapter.ProfileStateReadRequest{
		ProfileID: resource.ProfileID,
		Paths:     paths,
	})
	if err != nil {
		return ProfileBootstrapResult{}, err
	}
	empty, err := observation.PathsEmpty(paths)
	if err != nil {
		return ProfileBootstrapResult{}, err
	}
	if !empty {
		return ProfileBootstrapResult{}, nil
	}
	proposal, proposalCreated, err := workflow.planner.planResource(ctx, resource, observation)
	if err != nil {
		return ProfileBootstrapResult{}, err
	}
	result := ProfileBootstrapResult{
		ConditionMatched: true,
		Proposal:         proposal,
		ProposalCreated:  proposalCreated,
	}
	if proposal.Status == core.ProfileStateProposalNoChanges {
		return result, nil
	}
	task, taskCreated, err := workflow.apply.Enqueue(ctx, proposal.ID, source)
	if err != nil {
		return ProfileBootstrapResult{}, err
	}
	result.Task = task
	result.TaskCreated = taskCreated
	return result, nil
}
