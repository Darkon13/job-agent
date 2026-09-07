package core

import (
	"testing"
	"time"
)

func TestProfileActivityRecordHasStableIdentity(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	first, err := NewProfileActivityRecord("hh", "primary", "resume-1", ProfileActivityResumeTouched, "touch:2026-09-07T12", now)
	if err != nil {
		t.Fatalf("new activity: %v", err)
	}
	second, err := NewProfileActivityRecord("hh", "primary", "resume-1", ProfileActivityResumeTouched, "touch:2026-09-07T12", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("new repeated activity: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("activity identity changed with observation time: %s != %s", first.ID, second.ID)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("validate activity: %v", err)
	}
}

func TestProfileActivityRecordRejectsUnknownKind(t *testing.T) {
	_, err := NewProfileActivityRecord("hh", "primary", "", "guessed.score", "source-1", time.Now())
	if err == nil {
		t.Fatal("expected unknown activity kind to be rejected")
	}
}

func TestProfileActivitySnapshotDistinguishesMissingAndZeroCounters(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	snapshot, err := NewProfileActivitySnapshot("hh", "primary", "resume-1", "observe-1", now)
	if err != nil {
		t.Fatalf("new snapshot: %v", err)
	}
	zero := 0
	seven := 7
	snapshot.Views = &zero
	snapshot.PeriodDays = &seven
	snapshot.ScoreHidden = true
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("validate snapshot: %v", err)
	}
	if snapshot.SearchShows != nil || snapshot.Views == nil || *snapshot.Views != 0 {
		t.Fatalf("missing and zero counters were conflated: %#v", snapshot)
	}
}
