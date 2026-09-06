package workflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

func TestApplicationCampaignHandlerResumesSavedCursorAfterSQLiteRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	if err := storesqlite.MigrateUp(path); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	store, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	searcher := &cursorSearcher{pages: map[string]core.SearchPage{
		"":       {NextCursor: "page-2"},
		"page-2": {Done: true},
	}}
	ids := &sequentialIDs{}
	handler, err := NewApplicationCampaignHandler(store, store, store, store, fixedClock{now}, ids, time.Second)
	if err != nil {
		t.Fatalf("new campaign handler: %v", err)
	}
	registerCampaignSQLiteRoute(t, handler, searcher)
	start := campaignStartTask(t, now, []core.ProfileID{"profile"}, []core.SearchID{"primary"}, 1, 1)
	if err := handler.Handle(ctx, start); err != nil {
		t.Fatalf("first campaign page: %v", err)
	}
	campaignID := core.ApplicationCampaignID("campaign-" + string(start.ID))
	if err := store.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	reopened, err := storesqlite.Open(path)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := NewApplicationCampaignHandler(reopened, reopened, reopened, reopened, fixedClock{now.Add(time.Minute)}, &sequentialIDs{next: 100}, time.Second)
	if err != nil {
		t.Fatalf("new restarted handler: %v", err)
	}
	registerCampaignSQLiteRoute(t, restarted, searcher)
	key, err := core.ApplicationCampaignTickIdempotencyKey(campaignID, 2)
	if err != nil {
		t.Fatalf("tick key: %v", err)
	}
	tick, err := reopened.TaskByIdempotencyKey(ctx, key)
	if err != nil {
		t.Fatalf("load persisted tick: %v", err)
	}
	if err := restarted.Handle(ctx, tick); err != nil {
		t.Fatalf("resume campaign page: %v", err)
	}
	campaign, err := reopened.ApplicationCampaign(ctx, campaignID)
	if err != nil || campaign.Cursor != "" || !campaign.RouteDone || campaign.Revision != 3 {
		t.Fatalf("resumed campaign=%#v err=%v", campaign, err)
	}
	if len(searcher.cursors) != 2 || searcher.cursors[0] != "" || searcher.cursors[1] != "page-2" {
		t.Fatalf("searched cursors=%#v", searcher.cursors)
	}
}

func registerCampaignSQLiteRoute(t *testing.T, handler *ApplicationCampaignHandler, searcher *cursorSearcher) {
	t.Helper()
	if err := handler.Register(ApplicationCampaignRoute{
		SearchID: "primary", Platform: "hh", SearchProfileID: "profile",
		Query: json.RawMessage(`{}`), Searcher: searcher,
	}); err != nil {
		t.Fatalf("register campaign route: %v", err)
	}
}
