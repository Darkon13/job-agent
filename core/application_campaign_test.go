package core

import (
	"testing"
	"time"
)

func campaignFixture(t *testing.T) ApplicationCampaign {
	t.Helper()
	campaign, err := NewApplicationCampaign(NewApplicationCampaignParams{
		ID: "campaign-1", JobTag: "daily-backend",
		Profiles: []ProfileID{"primary"}, Routes: []SearchID{"golang", "backend"},
		TargetSuccessful: 20, MaxInFlight: 3, CorrelationID: "correlation-1",
	}, time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("new campaign: %v", err)
	}
	return campaign
}

func TestApplicationCampaignAdvancesPagesRoutesAndStops(t *testing.T) {
	campaign := campaignFixture(t)
	now := campaign.UpdatedAt
	if err := campaign.AdvancePage("page-2", false, now.Add(time.Minute)); err != nil {
		t.Fatalf("advance first page: %v", err)
	}
	if campaign.Cursor != "page-2" || campaign.Revision != 2 {
		t.Fatalf("first page state = %#v", campaign)
	}
	if err := campaign.AdvancePage("", true, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("finish route: %v", err)
	}
	if err := campaign.AdvanceRoute(now.Add(3 * time.Minute)); err != nil {
		t.Fatalf("advance route: %v", err)
	}
	if campaign.RouteIndex != 1 || campaign.Cursor != "" || campaign.RouteDone || campaign.Revision != 4 {
		t.Fatalf("next route state = %#v", campaign)
	}
	if err := campaign.Stop(ApplicationCampaignTargetReached, "target reached", now.Add(4*time.Minute)); err != nil {
		t.Fatalf("stop campaign: %v", err)
	}
	if campaign.Status != ApplicationCampaignTargetReached || campaign.StopReason != "target reached" || campaign.Revision != 5 {
		t.Fatalf("stopped campaign = %#v", campaign)
	}
}

func TestApplicationCampaignPausesAndResumes(t *testing.T) {
	campaign := campaignFixture(t)
	if err := campaign.Stop(ApplicationCampaignPausedRate, "retry after platform reset", campaign.UpdatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("pause campaign: %v", err)
	}
	if err := campaign.Resume(campaign.UpdatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("resume campaign: %v", err)
	}
	if campaign.Status != ApplicationCampaignRunning || campaign.StopReason != "" || campaign.Revision != 3 {
		t.Fatalf("resumed campaign = %#v", campaign)
	}
}

func TestApplicationCampaignRejectsInvalidDefinition(t *testing.T) {
	_, err := NewApplicationCampaign(NewApplicationCampaignParams{
		ID: "campaign-1", JobTag: "daily", Profiles: []ProfileID{"primary", "primary"},
		Routes: []SearchID{"golang"}, TargetSuccessful: 1, MaxInFlight: 1, CorrelationID: "correlation-1",
	}, time.Now().UTC())
	if err == nil {
		t.Fatal("expected duplicate profile validation error")
	}
}

func TestApplicationCampaignTickIdempotencyKeyIncludesRevision(t *testing.T) {
	first, err := ApplicationCampaignTickIdempotencyKey("campaign-1", 1)
	if err != nil {
		t.Fatalf("first key: %v", err)
	}
	repeated, _ := ApplicationCampaignTickIdempotencyKey("campaign-1", 1)
	next, _ := ApplicationCampaignTickIdempotencyKey("campaign-1", 2)
	if first != repeated || first == next {
		t.Fatalf("keys first=%q repeated=%q next=%q", first, repeated, next)
	}
}

func TestApplicationCampaignWaitAdvancesOnlyRevision(t *testing.T) {
	campaign := campaignFixture(t)
	before := campaign
	if err := campaign.WaitForApplications(before.UpdatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("wait for applications: %v", err)
	}
	if campaign.Revision != before.Revision+1 || campaign.RouteIndex != before.RouteIndex ||
		campaign.Cursor != before.Cursor || campaign.RouteDone != before.RouteDone {
		t.Fatalf("wait changed routing state: before=%#v after=%#v", before, campaign)
	}
}

func TestApplicationCampaignPayloadSeparatesStartFromTick(t *testing.T) {
	start := NewApplicationCampaignStartPayload("daily", []ProfileID{"primary"}, []SearchID{"golang"}, 20, 3)
	if err := start.Validate(); err != nil {
		t.Fatalf("valid start payload: %v", err)
	}
	tick := NewApplicationCampaignTickPayload("campaign-1", 2)
	if err := tick.Validate(); err != nil {
		t.Fatalf("valid tick payload: %v", err)
	}
	tick.JobTag = "changed"
	if err := tick.Validate(); err == nil {
		t.Fatal("expected tick redefinition to fail")
	}
}
