package memory

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestCreateSearchRunVersionsChangedDefinition(t *testing.T) {
	repository := NewRepository()
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	run, err := core.NewSearchRun(
		"golang", "hh-main", "hh", "primary", []core.ProfileID{"primary"},
		json.RawMessage(`{"source":"global","text":"Go"}`), "correlation-1", now,
	)
	if err != nil {
		t.Fatalf("new search run: %v", err)
	}
	stored, created, err := repository.CreateSearchRun(ctx, run)
	if err != nil || !created || stored.Generation != 1 || stored.Revision != 1 {
		t.Fatalf("create search run: stored=%#v created=%v err=%v", stored, created, err)
	}
	if err := stored.Advance("1", false, now.Add(time.Minute)); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := repository.SaveSearchRun(ctx, stored, 1); err != nil {
		t.Fatalf("save: %v", err)
	}

	changed, err := core.NewSearchRun(
		"golang", "hh-main", "hh", "primary", []core.ProfileID{"primary"},
		json.RawMessage(`{"source":"global","text":"Go AND Kafka"}`), "correlation-2", now.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("new changed run: %v", err)
	}
	reset, created, err := repository.CreateSearchRun(ctx, changed)
	if err != nil || !created {
		t.Fatalf("reset run: stored=%#v created=%v err=%v", reset, created, err)
	}
	if reset.Generation != 2 || reset.Revision != 3 || reset.Cursor != "" || reset.Done ||
		string(reset.Query) != `{"source":"global","text":"Go AND Kafka"}` || reset.CorrelationID != "correlation-2" {
		t.Fatalf("unexpected reset run: %#v", reset)
	}
	if !reset.CreatedAt.Equal(stored.CreatedAt) {
		t.Fatalf("reset changed created_at: %s != %s", reset.CreatedAt, stored.CreatedAt)
	}
	repeated, created, err := repository.CreateSearchRun(ctx, changed)
	if err != nil || created || repeated.Generation != 2 || repeated.Revision != 3 {
		t.Fatalf("repeat changed run: stored=%#v created=%v err=%v", repeated, created, err)
	}
}
