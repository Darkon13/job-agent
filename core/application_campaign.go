package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type ApplicationCampaignStatus string

const (
	ApplicationCampaignRunning       ApplicationCampaignStatus = "running"
	ApplicationCampaignTargetReached ApplicationCampaignStatus = "target_reached"
	ApplicationCampaignExhausted     ApplicationCampaignStatus = "exhausted"
	ApplicationCampaignPausedBudget  ApplicationCampaignStatus = "paused_budget"
	ApplicationCampaignPausedRate    ApplicationCampaignStatus = "paused_rate_limit"
	ApplicationCampaignFailed        ApplicationCampaignStatus = "failed"
)

// ApplicationCampaign is the durable routing cursor for one campaign
// occurrence. Application outcomes are read through CampaignApplication links
// instead of being copied into counters that could drift after a restart.
type ApplicationCampaign struct {
	ID               ApplicationCampaignID     `json:"id"`
	JobTag           string                    `json:"job_tag"`
	Profiles         []ProfileID               `json:"profiles"`
	Routes           []SearchID                `json:"routes"`
	TargetSuccessful int                       `json:"target_successful"`
	MaxInFlight      int                       `json:"max_in_flight"`
	RouteIndex       int                       `json:"route_index"`
	Cursor           string                    `json:"cursor,omitempty"`
	RouteDone        bool                      `json:"route_done"`
	Status           ApplicationCampaignStatus `json:"status"`
	StopReason       string                    `json:"stop_reason,omitempty"`
	CorrelationID    CorrelationID             `json:"correlation_id"`
	Revision         uint64                    `json:"revision"`
	CreatedAt        time.Time                 `json:"created_at"`
	UpdatedAt        time.Time                 `json:"updated_at"`
}

type NewApplicationCampaignParams struct {
	ID               ApplicationCampaignID
	JobTag           string
	Profiles         []ProfileID
	Routes           []SearchID
	TargetSuccessful int
	MaxInFlight      int
	CorrelationID    CorrelationID
}

func NewApplicationCampaign(params NewApplicationCampaignParams, now time.Time) (ApplicationCampaign, error) {
	campaign := ApplicationCampaign{
		ID: params.ID, JobTag: strings.TrimSpace(params.JobTag),
		Profiles: slices.Clone(params.Profiles), Routes: slices.Clone(params.Routes),
		TargetSuccessful: params.TargetSuccessful, MaxInFlight: params.MaxInFlight,
		Status: ApplicationCampaignRunning, CorrelationID: params.CorrelationID,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := campaign.Validate(); err != nil {
		return ApplicationCampaign{}, err
	}
	return campaign, nil
}

func (campaign ApplicationCampaign) Validate() error {
	if campaign.ID == "" || campaign.JobTag == "" || campaign.CorrelationID == "" {
		return errors.New("application campaign requires id, job tag and correlation id")
	}
	if err := validateCampaignIDs(campaign.Profiles, "profile"); err != nil {
		return err
	}
	if err := validateCampaignIDs(campaign.Routes, "route"); err != nil {
		return err
	}
	if campaign.TargetSuccessful < 1 || campaign.MaxInFlight < 1 {
		return errors.New("application campaign target and max in flight must be positive")
	}
	if campaign.RouteIndex < 0 || campaign.RouteIndex >= len(campaign.Routes) {
		return errors.New("application campaign route index is out of bounds")
	}
	if campaign.RouteDone && campaign.Cursor != "" {
		return errors.New("completed campaign route must not retain a cursor")
	}
	switch campaign.Status {
	case ApplicationCampaignRunning:
		if campaign.StopReason != "" {
			return errors.New("running application campaign must not have a stop reason")
		}
	case ApplicationCampaignTargetReached, ApplicationCampaignExhausted,
		ApplicationCampaignPausedBudget, ApplicationCampaignPausedRate, ApplicationCampaignFailed:
		if strings.TrimSpace(campaign.StopReason) == "" {
			return errors.New("stopped application campaign requires a reason")
		}
	default:
		return fmt.Errorf("unknown application campaign status %q", campaign.Status)
	}
	if campaign.Revision == 0 || campaign.CreatedAt.IsZero() || campaign.UpdatedAt.IsZero() || campaign.UpdatedAt.Before(campaign.CreatedAt) {
		return errors.New("application campaign requires valid revision and timestamps")
	}
	return nil
}

func validateCampaignIDs[T ~string](values []T, label string) error {
	if len(values) == 0 {
		return fmt.Errorf("application campaign requires at least one %s", label)
	}
	seen := make(map[T]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(string(value)) == "" {
			return fmt.Errorf("application campaign contains an empty %s", label)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("application campaign contains duplicate %s %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func (campaign *ApplicationCampaign) AdvancePage(nextCursor string, done bool, now time.Time) error {
	if campaign == nil {
		return errors.New("application campaign is nil")
	}
	if campaign.Status != ApplicationCampaignRunning || campaign.RouteDone {
		return errors.New("application campaign route cannot advance")
	}
	if done && nextCursor != "" {
		return errors.New("completed campaign page must not contain a cursor")
	}
	if !done && strings.TrimSpace(nextCursor) == "" {
		return errors.New("incomplete campaign page requires a cursor")
	}
	if err := campaign.advanceRevision(now); err != nil {
		return err
	}
	campaign.Cursor = nextCursor
	campaign.RouteDone = done
	return campaign.Validate()
}

func (campaign *ApplicationCampaign) AdvanceRoute(now time.Time) error {
	if campaign == nil {
		return errors.New("application campaign is nil")
	}
	if campaign.Status != ApplicationCampaignRunning || !campaign.RouteDone || campaign.RouteIndex+1 >= len(campaign.Routes) {
		return errors.New("application campaign has no completed route to advance")
	}
	if err := campaign.advanceRevision(now); err != nil {
		return err
	}
	campaign.RouteIndex++
	campaign.Cursor = ""
	campaign.RouteDone = false
	return campaign.Validate()
}

// WaitForApplications advances the durable tick generation without moving the
// search cursor. It gives a later reconciliation task a fresh idempotency key
// while application tasks occupy all available slots.
func (campaign *ApplicationCampaign) WaitForApplications(now time.Time) error {
	if campaign == nil {
		return errors.New("application campaign is nil")
	}
	if campaign.Status != ApplicationCampaignRunning {
		return errors.New("only a running application campaign can wait")
	}
	if err := campaign.advanceRevision(now); err != nil {
		return err
	}
	return campaign.Validate()
}

func (campaign *ApplicationCampaign) Stop(status ApplicationCampaignStatus, reason string, now time.Time) error {
	if campaign == nil {
		return errors.New("application campaign is nil")
	}
	if campaign.Status != ApplicationCampaignRunning {
		return errors.New("only a running application campaign can stop")
	}
	switch status {
	case ApplicationCampaignTargetReached, ApplicationCampaignExhausted,
		ApplicationCampaignPausedBudget, ApplicationCampaignPausedRate, ApplicationCampaignFailed:
	default:
		return errors.New("application campaign requires a terminal or paused status")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("application campaign stop requires a reason")
	}
	if err := campaign.advanceRevision(now); err != nil {
		return err
	}
	campaign.Status = status
	campaign.StopReason = reason
	return campaign.Validate()
}

func (campaign *ApplicationCampaign) Resume(now time.Time) error {
	if campaign == nil {
		return errors.New("application campaign is nil")
	}
	if campaign.Status != ApplicationCampaignPausedBudget && campaign.Status != ApplicationCampaignPausedRate {
		return errors.New("only a paused application campaign can resume")
	}
	if err := campaign.advanceRevision(now); err != nil {
		return err
	}
	campaign.Status = ApplicationCampaignRunning
	campaign.StopReason = ""
	return campaign.Validate()
}

func (campaign *ApplicationCampaign) advanceRevision(now time.Time) error {
	if now.IsZero() || now.Before(campaign.UpdatedAt) {
		return errors.New("application campaign update time must not move backwards")
	}
	campaign.Revision++
	campaign.UpdatedAt = now
	return nil
}

type CampaignApplication struct {
	CampaignID    ApplicationCampaignID `json:"campaign_id"`
	RouteIndex    int                   `json:"route_index"`
	ApplicationID ApplicationID         `json:"application_id"`
	DiscoveredAt  time.Time             `json:"discovered_at"`
}

type CampaignApplicationState struct {
	Link        CampaignApplication `json:"link"`
	Application Application         `json:"application"`
}

type ApplicationCampaignProgress struct {
	Planned   int `json:"planned"`
	InFlight  int `json:"in_flight"`
	Submitted int `json:"submitted"`
	Blocked   int `json:"blocked"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
}

func (item CampaignApplication) Validate() error {
	if item.CampaignID == "" || item.ApplicationID == "" || item.RouteIndex < 0 || item.DiscoveredAt.IsZero() {
		return errors.New("campaign application requires campaign, route, application and discovery time")
	}
	return nil
}

func ApplicationCampaignTickIdempotencyKey(campaignID ApplicationCampaignID, revision uint64) (string, error) {
	if campaignID == "" || revision == 0 {
		return "", errors.New("campaign tick idempotency requires campaign and revision")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", campaignID, revision)))
	return "application.campaign:" + hex.EncodeToString(digest[:]), nil
}
