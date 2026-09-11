package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type fakeQualificationCatalog struct {
	offerings []core.QualificationOffering
	err       error
	calls     int
}

func (fake *fakeQualificationCatalog) SyncQualifications(context.Context, core.ProfileID) ([]core.QualificationOffering, error) {
	fake.calls++
	return fake.offerings, fake.err
}

func qualificationSyncTask(t *testing.T, profileID core.ProfileID) core.Task {
	t.Helper()
	payload, err := json.Marshal(core.SkillVerificationSyncPayload{ProfileID: profileID})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return core.Task{
		ID: "sync-task", Type: core.TaskSkillVerificationSync, ProfileID: profileID,
		CorrelationID: "correlation-1", IdempotencyKey: "sync-1", Payload: payload,
	}
}

func TestQualificationSyncStoresObservedCatalog(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	order := 1
	offering := core.QualificationOffering{
		ID: "offer-1", Platform: "hh", ProfileID: "primary", ExternalID: "go-medium",
		Qualification: core.QualificationDescriptor{
			FamilyID: "go", FamilyName: "Go", LevelID: "medium", LevelName: "Средний", LevelOrder: &order,
		},
		Status: core.QualificationAvailable, ObservedAt: now,
	}
	reader := &fakeQualificationCatalog{offerings: []core.QualificationOffering{offering}}
	registry := NewQualificationCatalogRegistry()
	if err := registry.Register("primary", reader); err != nil {
		t.Fatalf("register: %v", err)
	}
	repository := storagememory.NewRepository()
	handler, err := NewQualificationSyncHandler(registry, repository)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if err := handler.Handle(context.Background(), qualificationSyncTask(t, "primary")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	offerings, err := repository.QualificationOfferings(context.Background(), "hh", "primary")
	if err != nil || len(offerings) != 1 || offerings[0].ID != "offer-1" {
		t.Fatalf("offerings = %#v err=%v", offerings, err)
	}

	reader.offerings = nil
	if err := handler.Handle(context.Background(), qualificationSyncTask(t, "primary")); err != nil {
		t.Fatalf("empty sync: %v", err)
	}
	if err := handler.Handle(context.Background(), qualificationSyncTask(t, "unknown")); err == nil {
		t.Fatal("expected unknown profile to fail")
	} else {
		var operationErr *core.OperationError
		if !errors.As(err, &operationErr) || operationErr.Category != core.ErrorUnsupported {
			t.Fatalf("error = %v", err)
		}
	}
}
