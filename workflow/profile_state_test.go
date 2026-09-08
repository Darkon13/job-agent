package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage/memory"
)

type profileStateReaderFunc func(context.Context, adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error)

func (function profileStateReaderFunc) ReadProfileState(ctx context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
	return function(ctx, request)
}

func TestProfileStatePlannerPersistsStableProposal(t *testing.T) {
	now := time.Date(2026, 9, 7, 14, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"backend":{"about":"new"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	repository := memory.NewRepository()
	planner, err := NewProfileStatePlanner([]core.ProfileStateResource{resource}, repository, fixedClock{now}, &sequentialIDs{})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}
	observation, err := core.NewProfileStateObservation("primary", json.RawMessage(`{"resumes":{"backend":{"about":"old"}}}`), "revision-1", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	first, created, err := planner.Plan(context.Background(), "backend", observation)
	if err != nil || !created {
		t.Fatalf("first plan: %#v created=%v err=%v", first, created, err)
	}
	repeatedObservation, err := core.NewProfileStateObservation("primary", observation.State, "revision-1", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("new repeated observation: %v", err)
	}
	second, created, err := planner.Plan(context.Background(), "backend", repeatedObservation)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("repeated plan: %#v created=%v err=%v", second, created, err)
	}
	resources := planner.Resources()
	resources[0].State[0] = '['
	stored, exists := planner.Resource("backend")
	if !exists || string(stored.State) != string(resource.State) {
		t.Fatalf("planner resource was mutated: %#v", stored)
	}
}

func TestProfileStatePlannerRejectsAnotherProfile(t *testing.T) {
	now := time.Date(2026, 9, 7, 14, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"profile":{"first_name":"Иван"}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	planner, err := NewProfileStatePlanner([]core.ProfileStateResource{resource}, memory.NewRepository(), fixedClock{now}, &sequentialIDs{})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}
	observation, err := core.NewProfileStateObservation("secondary", json.RawMessage(`{"profile":{}}`), "", now)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	if _, _, err := planner.Plan(context.Background(), "backend", observation); err == nil {
		t.Fatal("expected profile mismatch to fail")
	}
}

func TestProfileStatePlannerReadsOnlyDeclaredPathsBeforePlanning(t *testing.T) {
	now := time.Date(2026, 9, 7, 14, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"new"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	planner, err := NewProfileStatePlanner([]core.ProfileStateResource{resource}, memory.NewRepository(), fixedClock{now}, &sequentialIDs{})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}
	reader := profileStateReaderFunc(func(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		if request.ProfileID != "primary" || len(request.Paths) != 1 || request.Paths[0] != "/resumes/resume-1/about" {
			t.Fatalf("read request = %#v", request)
		}
		return core.NewProfileStateObservation(request.ProfileID, json.RawMessage(`{"resumes":{"resume-1":{"about":"old"}}}`), "", now)
	})
	proposal, created, err := planner.ReadAndPlan(context.Background(), "backend", reader)
	if err != nil || !created || proposal.Status != core.ProfileStateProposalPlanned {
		t.Fatalf("read and plan: %#v created=%v err=%v", proposal, created, err)
	}
}

func TestProfileStatePlannerUsesOneShotOverrideWithoutMutatingSource(t *testing.T) {
	now := time.Date(2026, 9, 7, 14, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("backend", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"about":"from config"}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	repository := memory.NewRepository()
	planner, err := NewProfileStatePlanner([]core.ProfileStateResource{resource}, repository, fixedClock{now}, &sequentialIDs{})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}
	reader := profileStateReaderFunc(func(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		return core.NewProfileStateObservation(request.ProfileID, json.RawMessage(`{"resumes":{"resume-1":{"about":"current"}}}`), "", now)
	})
	proposal, created, err := planner.ReadAndPlanWithOverrides(context.Background(), "backend", []core.ProfileStateValueOverride{{
		Path: "/resumes/resume-1/about", Value: json.RawMessage(`"from dashboard"`),
	}}, reader)
	if err != nil || !created {
		t.Fatalf("override plan: %#v created=%v err=%v", proposal, created, err)
	}
	if string(proposal.DesiredState) != `{"resumes":{"resume-1":{"about":"from dashboard"}}}` {
		t.Fatalf("proposal desired state = %s", proposal.DesiredState)
	}
	storedResource, exists := planner.Resource("backend")
	if !exists || string(storedResource.State) != string(resource.State) || storedResource.ManifestDigest != resource.ManifestDigest {
		t.Fatalf("registered resource changed: %#v", storedResource)
	}
}

func TestProfileStatePlannerPlansUnregisteredBootstrapResource(t *testing.T) {
	now := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("bootstrap", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{"resumes":{"resume-1":{"skill_set":["Go","PostgreSQL"]}}}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	repository := memory.NewRepository()
	planner, err := NewProfileStatePlanner(nil, repository, fixedClock{now}, &sequentialIDs{})
	if err != nil {
		t.Fatalf("new planner: %v", err)
	}
	reader := profileStateReaderFunc(func(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		if request.ProfileID != "primary" || len(request.Paths) != 1 || request.Paths[0] != "/resumes/resume-1/skill_set" {
			t.Fatalf("read request = %#v", request)
		}
		return core.NewProfileStateObservation(request.ProfileID, json.RawMessage(`{"resumes":{"resume-1":{"skill_set":["Go"]}}}`), "revision-1", now)
	})
	proposal, created, err := planner.ReadAndPlanResource(context.Background(), resource, reader)
	if err != nil || !created || proposal.Status != core.ProfileStateProposalPlanned {
		t.Fatalf("bootstrap plan: %#v created=%v err=%v", proposal, created, err)
	}
	if _, exists := planner.Resource(resource.Tag); exists {
		t.Fatal("one-shot resource was registered")
	}
	stored, err := repository.ProfileStateProposal(context.Background(), proposal.ID)
	if err != nil || string(stored.DesiredState) != string(resource.State) {
		t.Fatalf("stored proposal = %#v err=%v", stored, err)
	}
}
