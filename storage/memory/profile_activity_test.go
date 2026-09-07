package memory

import (
	"context"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func TestProfileActivityIsIdempotentAndAggregated(t *testing.T) {
	repository := NewRepository()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	record, err := core.NewProfileActivityRecord("hh", "primary", "resume-1", core.ProfileActivityResumeTouched, "touch-1", now)
	if err != nil {
		t.Fatalf("new activity: %v", err)
	}
	if created, err := repository.RecordProfileActivity(context.Background(), record); err != nil || !created {
		t.Fatalf("record activity: created=%t err=%v", created, err)
	}
	repeated := record
	repeated.OccurredAt = now.Add(time.Minute)
	if created, err := repository.RecordProfileActivity(context.Background(), repeated); err != nil || created {
		t.Fatalf("repeat activity: created=%t err=%v", created, err)
	}
	counts, err := repository.ProfileActivityCounts(context.Background(), storage.ProfileActivityFilter{ProfileID: "primary"})
	if err != nil || len(counts) != 1 || counts[0].Count != 1 || !counts[0].LastOccurredAt.Equal(now) {
		t.Fatalf("activity counts=%#v err=%v", counts, err)
	}
}

func TestProfileActivitySnapshotsAreBoundedAndNewestFirst(t *testing.T) {
	repository := NewRepository()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	for index, source := range []string{"observe-1", "observe-2"} {
		snapshot, err := core.NewProfileActivitySnapshot("hh", "primary", "resume-1", source, now.Add(time.Duration(index)*time.Hour))
		if err != nil {
			t.Fatalf("new snapshot: %v", err)
		}
		views := index
		snapshot.Views = &views
		if created, err := repository.RecordProfileActivitySnapshot(context.Background(), snapshot); err != nil || !created {
			t.Fatalf("record snapshot: created=%t err=%v", created, err)
		}
	}
	items, err := repository.ListProfileActivitySnapshots(context.Background(), storage.ProfileActivitySnapshotFilter{ProfileID: "primary", Limit: 1})
	if err != nil || len(items) != 1 || items[0].SourceID != "observe-2" || items[0].Views == nil || *items[0].Views != 1 {
		t.Fatalf("snapshots=%#v err=%v", items, err)
	}
}
