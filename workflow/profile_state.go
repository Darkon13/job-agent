package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ProfileStatePlanner is intentionally side-effect free with respect to the
// external platform. A caller must supply an observation produced by a trusted
// adapter reader; planning only calculates and persists an immutable proposal.
type ProfileStatePlanner struct {
	repository storage.ProfileStateProposalRepository
	clock      Clock
	ids        IDGenerator
	resources  map[string]core.ProfileStateResource
}

func NewProfileStatePlanner(resources []core.ProfileStateResource, repository storage.ProfileStateProposalRepository, clock Clock, ids IDGenerator) (*ProfileStatePlanner, error) {
	if repository == nil || clock == nil || ids == nil {
		return nil, errors.New("profile state planner requires repository, clock and id generator")
	}
	indexed := make(map[string]core.ProfileStateResource, len(resources))
	for _, resource := range resources {
		if err := resource.Validate(); err != nil {
			return nil, fmt.Errorf("profile state resource %q: %w", resource.Tag, err)
		}
		if _, exists := indexed[resource.Tag]; exists {
			return nil, fmt.Errorf("duplicate profile state resource %q", resource.Tag)
		}
		indexed[resource.Tag] = cloneProfileStateResource(resource)
	}
	return &ProfileStatePlanner{repository: repository, clock: clock, ids: ids, resources: indexed}, nil
}

func (planner *ProfileStatePlanner) Resources() []core.ProfileStateResource {
	if planner == nil {
		return nil
	}
	result := make([]core.ProfileStateResource, 0, len(planner.resources))
	for _, resource := range planner.resources {
		result = append(result, cloneProfileStateResource(resource))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Tag < result[j].Tag })
	return result
}

func (planner *ProfileStatePlanner) Resource(tag string) (core.ProfileStateResource, bool) {
	if planner == nil {
		return core.ProfileStateResource{}, false
	}
	resource, exists := planner.resources[strings.TrimSpace(tag)]
	return cloneProfileStateResource(resource), exists
}

func (planner *ProfileStatePlanner) Plan(ctx context.Context, resourceTag string, observation core.ProfileStateObservation) (core.ProfileStateProposal, bool, error) {
	if planner == nil {
		return core.ProfileStateProposal{}, false, errors.New("profile state planner is nil")
	}
	resource, exists := planner.resources[strings.TrimSpace(resourceTag)]
	if !exists {
		return core.ProfileStateProposal{}, false, fmt.Errorf("profile state resource %q is not registered", resourceTag)
	}
	return planner.planResource(ctx, resource, observation)
}

func (planner *ProfileStatePlanner) planResource(ctx context.Context, resource core.ProfileStateResource, observation core.ProfileStateObservation) (core.ProfileStateProposal, bool, error) {
	if observation.ProfileID != resource.ProfileID {
		return core.ProfileStateProposal{}, false, errors.New("profile state observation belongs to another profile")
	}
	id, err := planner.ids.NewID("profile-state-proposal")
	if err != nil {
		return core.ProfileStateProposal{}, false, err
	}
	proposal, err := core.NewProfileStateProposal(core.ProfileStateProposalID(id), resource, observation, planner.clock.Now())
	if err != nil {
		return core.ProfileStateProposal{}, false, err
	}
	return planner.repository.CreateProfileStateProposal(ctx, proposal)
}

// ReadAndPlan asks a trusted profile-scoped adapter for exactly the fields
// declared by the resource, then creates a proposal from that observation.
func (planner *ProfileStatePlanner) ReadAndPlan(ctx context.Context, resourceTag string, reader adapter.ProfileStateReader) (core.ProfileStateProposal, bool, error) {
	if reader == nil {
		return core.ProfileStateProposal{}, false, errors.New("profile state reader is nil")
	}
	resource, exists := planner.Resource(resourceTag)
	if !exists {
		return core.ProfileStateProposal{}, false, fmt.Errorf("profile state resource %q is not registered", resourceTag)
	}
	return planner.readAndPlanResource(ctx, resource, reader)
}

// ReadAndPlanResource plans an explicitly supplied one-shot manifest. Unlike
// scheduled resources, it is not added to the planner registry; the immutable
// proposal stored by planResource is the complete source of truth for apply.
func (planner *ProfileStatePlanner) ReadAndPlanResource(ctx context.Context, resource core.ProfileStateResource, reader adapter.ProfileStateReader) (core.ProfileStateProposal, bool, error) {
	if planner == nil {
		return core.ProfileStateProposal{}, false, errors.New("profile state planner is nil")
	}
	if err := resource.Validate(); err != nil {
		return core.ProfileStateProposal{}, false, err
	}
	return planner.readAndPlanResource(ctx, resource, reader)
}

// ReadAndPlanWithOverrides creates an immutable one-shot proposal without
// changing the registered resource that remains the persistent source of
// truth. The derived resource retains exactly the same declared ownership.
func (planner *ProfileStatePlanner) ReadAndPlanWithOverrides(ctx context.Context, resourceTag string, overrides []core.ProfileStateValueOverride, reader adapter.ProfileStateReader) (core.ProfileStateProposal, bool, error) {
	if planner == nil {
		return core.ProfileStateProposal{}, false, errors.New("profile state planner is nil")
	}
	resource, exists := planner.Resource(resourceTag)
	if !exists {
		return core.ProfileStateProposal{}, false, fmt.Errorf("profile state resource %q is not registered", resourceTag)
	}
	derived, err := resource.WithOverrides(overrides)
	if err != nil {
		return core.ProfileStateProposal{}, false, err
	}
	return planner.readAndPlanResource(ctx, derived, reader)
}

func (planner *ProfileStatePlanner) readAndPlanResource(ctx context.Context, resource core.ProfileStateResource, reader adapter.ProfileStateReader) (core.ProfileStateProposal, bool, error) {
	if reader == nil {
		return core.ProfileStateProposal{}, false, errors.New("profile state reader is nil")
	}
	paths, err := resource.DeclaredPaths()
	if err != nil {
		return core.ProfileStateProposal{}, false, err
	}
	observation, err := reader.ReadProfileState(ctx, adapter.ProfileStateReadRequest{ProfileID: resource.ProfileID, Paths: paths})
	if err != nil {
		return core.ProfileStateProposal{}, false, err
	}
	return planner.planResource(ctx, resource, observation)
}

func cloneProfileStateResource(resource core.ProfileStateResource) core.ProfileStateResource {
	resource.State = append([]byte(nil), resource.State...)
	return resource
}
