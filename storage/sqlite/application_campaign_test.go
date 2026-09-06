package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func TestStorePersistsApplicationCampaignAndLinks(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	vacancy := core.Vacancy{Platform: "hh", ExternalID: "42", Title: "Go", State: core.VacancyStateOpen, ObservedAt: now}
	if _, err := store.UpsertVacancy(ctx, vacancy); err != nil {
		t.Fatalf("store vacancy: %v", err)
	}
	application, err := core.NewApplication("application-1", core.ApplicationKey{ProfileID: "primary", Vacancy: vacancy.Key()}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, _, err := store.CreateApplication(ctx, application); err != nil {
		t.Fatalf("create application: %v", err)
	}
	campaign, err := core.NewApplicationCampaign(core.NewApplicationCampaignParams{
		ID: "campaign-1", JobTag: "daily", Profiles: []core.ProfileID{"primary"},
		Routes: []core.SearchID{"golang", "fallback"}, TargetSuccessful: 10,
		MaxInFlight: 2, CorrelationID: "correlation-1",
	}, now)
	if err != nil {
		t.Fatalf("new campaign: %v", err)
	}
	stored, created, err := store.CreateApplicationCampaign(ctx, campaign)
	if err != nil || !created {
		t.Fatalf("create campaign: stored=%#v created=%v err=%v", stored, created, err)
	}
	item := core.CampaignApplication{CampaignID: campaign.ID, RouteIndex: 0, ApplicationID: application.ID, DiscoveredAt: now}
	if created, err := store.LinkCampaignApplication(ctx, item); err != nil || !created {
		t.Fatalf("link application: created=%v err=%v", created, err)
	}
	if err := stored.AdvancePage("page-2", false, now.Add(time.Minute)); err != nil {
		t.Fatalf("advance campaign: %v", err)
	}
	if err := store.SaveApplicationCampaign(ctx, stored, 1); err != nil {
		t.Fatalf("save campaign: %v", err)
	}
	if err := store.SaveApplicationCampaign(ctx, stored, 1); !errors.Is(err, storage.ErrRevisionConflict) {
		t.Fatalf("stale save error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := openStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.ApplicationCampaign(ctx, campaign.ID)
	if err != nil || persisted.Cursor != "page-2" || persisted.Revision != 2 || persisted.MaxInFlight != 2 {
		t.Fatalf("persisted campaign=%#v err=%v", persisted, err)
	}
	items, err := reopened.ListCampaignApplications(ctx, campaign.ID)
	if err != nil || len(items) != 1 || items[0].ApplicationID != application.ID {
		t.Fatalf("persisted items=%#v err=%v", items, err)
	}
	states, err := reopened.ListCampaignApplicationStates(ctx, campaign.ID)
	if err != nil || len(states) != 1 || states[0].Application.ID != application.ID || states[0].Application.Key != application.Key {
		t.Fatalf("persisted application states=%#v err=%v", states, err)
	}
	stats, err := reopened.Stats(ctx)
	if err != nil || stats.ApplicationCampaigns != 1 || stats.CampaignApplications != 1 {
		t.Fatalf("stats=%#v err=%v", stats, err)
	}
}
