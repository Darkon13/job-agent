package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func TestApplicationCampaignRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	repository := NewRepository()
	campaign, err := core.NewApplicationCampaign(core.NewApplicationCampaignParams{
		ID: "campaign-1", JobTag: "daily", Profiles: []core.ProfileID{"primary"},
		Routes: []core.SearchID{"golang", "fallback"}, TargetSuccessful: 10,
		MaxInFlight: 2, CorrelationID: "correlation-1",
	}, now)
	if err != nil {
		t.Fatalf("new campaign: %v", err)
	}
	stored, created, err := repository.CreateApplicationCampaign(ctx, campaign)
	if err != nil || !created || stored.Revision != 1 {
		t.Fatalf("create campaign: stored=%#v created=%v err=%v", stored, created, err)
	}
	repeated := campaign
	repeated.CreatedAt = repeated.CreatedAt.Add(time.Hour)
	repeated.UpdatedAt = repeated.CreatedAt
	if _, created, err := repository.CreateApplicationCampaign(ctx, repeated); err != nil || created {
		t.Fatalf("repeat campaign: created=%v err=%v", created, err)
	}
	if err := stored.AdvancePage("page-2", false, now.Add(time.Minute)); err != nil {
		t.Fatalf("advance campaign: %v", err)
	}
	if err := repository.SaveApplicationCampaign(ctx, stored, 1); err != nil {
		t.Fatalf("save campaign: %v", err)
	}
	campaigns, err := repository.ListApplicationCampaigns(ctx, 1)
	if err != nil || len(campaigns) != 1 || campaigns[0].ID != campaign.ID || campaigns[0].Revision != 2 {
		t.Fatalf("campaign list=%#v err=%v", campaigns, err)
	}
	if _, err := repository.ListApplicationCampaigns(ctx, 0); err == nil {
		t.Fatal("expected invalid campaign list limit to fail")
	}
	if err := repository.SaveApplicationCampaign(ctx, stored, 1); !errors.Is(err, storage.ErrRevisionConflict) {
		t.Fatalf("stale save error = %v", err)
	}

	application, err := core.NewApplication("application-1", core.ApplicationKey{
		ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "42"},
	}, now)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, _, err := repository.CreateApplication(ctx, application); err != nil {
		t.Fatalf("create application: %v", err)
	}
	item := core.CampaignApplication{CampaignID: campaign.ID, RouteIndex: 0, ApplicationID: application.ID, DiscoveredAt: now}
	if created, err := repository.LinkCampaignApplication(ctx, item); err != nil || !created {
		t.Fatalf("link application: created=%v err=%v", created, err)
	}
	if created, err := repository.LinkCampaignApplication(ctx, item); err != nil || created {
		t.Fatalf("repeat link: created=%v err=%v", created, err)
	}
	items, err := repository.ListCampaignApplications(ctx, campaign.ID)
	if err != nil || len(items) != 1 || items[0].ApplicationID != application.ID {
		t.Fatalf("campaign items=%#v err=%v", items, err)
	}
	states, err := repository.ListCampaignApplicationStates(ctx, campaign.ID)
	if err != nil || len(states) != 1 || states[0].Application.ID != application.ID || states[0].Application.Key != application.Key {
		t.Fatalf("campaign application states=%#v err=%v", states, err)
	}
}
