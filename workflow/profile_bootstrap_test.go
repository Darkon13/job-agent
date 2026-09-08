package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

func TestProfileBootstrapWorkflowEnqueuesInitialFillFromOneObservation(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("primary-bootstrap", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{
		"profile":{"area":[1]},
		"resumes":{"resume-1":{"web":{"skills":["Backend"]}}}
	}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	planner, _ := NewProfileStatePlanner(nil, repository, fixedClock{now}, &sequentialIDs{})
	apply, _ := NewProfileStateApplyWorkflow(repository, queue, fixedClock{now}, &sequentialIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	bootstrap, err := NewProfileBootstrapWorkflow(planner, apply)
	if err != nil {
		t.Fatalf("new bootstrap workflow: %v", err)
	}
	reads := 0
	reader := profileStateReaderFunc(func(_ context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		reads++
		if got := strings.Join(request.Paths, ","); got != "/profile/area,/resumes/resume-1/web/skills" {
			t.Fatalf("paths = %q", got)
		}
		return core.NewProfileStateObservation("primary", json.RawMessage(`{"profile":{"area":[]},"resumes":{}}`), "revision-1", now)
	})
	result, err := bootstrap.RunWhenEmpty(context.Background(), resource, reader, "config:profile-bootstrap")
	if err != nil || !result.ConditionMatched || !result.ProposalCreated || !result.TaskCreated {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
	if reads != 1 || result.Task.Type != core.TaskProfileStateApply || result.Task.ProfileID != "primary" {
		t.Fatalf("reads=%d task=%#v", reads, result.Task)
	}
}

func TestProfileBootstrapWorkflowSkipsPartiallyPopulatedState(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	resource, err := core.NewProfileStateResource("primary-bootstrap", "primary", core.ProfileStateOwnershipDeclaredFields, json.RawMessage(`{
		"profile":{"area":[1],"firstName":["Иван"]}
	}`))
	if err != nil {
		t.Fatalf("new resource: %v", err)
	}
	repository := storagememory.NewRepository()
	queue := brokermemory.NewQueue()
	planner, _ := NewProfileStatePlanner(nil, repository, fixedClock{now}, &sequentialIDs{})
	apply, _ := NewProfileStateApplyWorkflow(repository, queue, fixedClock{now}, &sequentialIDs{}, map[core.ProfileID]core.Platform{"primary": "hh"})
	bootstrap, _ := NewProfileBootstrapWorkflow(planner, apply)
	reader := profileStateReaderFunc(func(_ context.Context, _ adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
		return core.NewProfileStateObservation("primary", json.RawMessage(`{"profile":{"area":[],"firstName":["Уже заполнено"]}}`), "", now)
	})
	result, err := bootstrap.RunWhenEmpty(context.Background(), resource, reader, "config:profile-bootstrap")
	if err != nil || result.ConditionMatched || result.Proposal.ID != "" || result.Task.ID != "" {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
}
