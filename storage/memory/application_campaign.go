package memory

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type campaignApplicationKey struct {
	campaignID    core.ApplicationCampaignID
	applicationID core.ApplicationID
}

func (repository *Repository) CreateApplicationCampaign(ctx context.Context, candidate core.ApplicationCampaign) (core.ApplicationCampaign, bool, error) {
	if err := ctx.Err(); err != nil {
		return core.ApplicationCampaign{}, false, err
	}
	if err := candidate.Validate(); err != nil {
		return core.ApplicationCampaign{}, false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if stored, exists := repository.applicationCampaigns[candidate.ID]; exists {
		if !sameCampaignDefinition(stored, candidate) {
			return core.ApplicationCampaign{}, false, errors.New("application campaign conflicts with changed definition")
		}
		return cloneApplicationCampaign(stored), false, nil
	}
	repository.applicationCampaigns[candidate.ID] = cloneApplicationCampaign(candidate)
	return cloneApplicationCampaign(candidate), true, nil
}

func (repository *Repository) ApplicationCampaign(ctx context.Context, id core.ApplicationCampaignID) (core.ApplicationCampaign, error) {
	if err := ctx.Err(); err != nil {
		return core.ApplicationCampaign{}, err
	}
	if id == "" {
		return core.ApplicationCampaign{}, errors.New("application campaign requires id")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	campaign, exists := repository.applicationCampaigns[id]
	if !exists {
		return core.ApplicationCampaign{}, errors.New("application campaign not found")
	}
	return cloneApplicationCampaign(campaign), nil
}

func (repository *Repository) ListApplicationCampaigns(ctx context.Context, limit int) ([]core.ApplicationCampaign, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("application campaign list limit must be between 1 and 100")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	campaigns := make([]core.ApplicationCampaign, 0, len(repository.applicationCampaigns))
	for _, campaign := range repository.applicationCampaigns {
		campaigns = append(campaigns, cloneApplicationCampaign(campaign))
	}
	sort.Slice(campaigns, func(i, j int) bool {
		if !campaigns[i].UpdatedAt.Equal(campaigns[j].UpdatedAt) {
			return campaigns[i].UpdatedAt.After(campaigns[j].UpdatedAt)
		}
		return campaigns[i].ID > campaigns[j].ID
	})
	if len(campaigns) > limit {
		campaigns = campaigns[:limit]
	}
	return campaigns, nil
}

func (repository *Repository) SaveApplicationCampaign(ctx context.Context, candidate core.ApplicationCampaign, expectedRevision uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Revision != expectedRevision+1 {
		return errors.New("application campaign revision must advance by one")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.applicationCampaigns[candidate.ID]
	if !exists || stored.Revision != expectedRevision {
		return storage.ErrRevisionConflict
	}
	if !sameCampaignDefinition(stored, candidate) {
		return errors.New("application campaign immutable definition changed")
	}
	repository.applicationCampaigns[candidate.ID] = cloneApplicationCampaign(candidate)
	return nil
}

func (repository *Repository) LinkCampaignApplication(ctx context.Context, item core.CampaignApplication) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := item.Validate(); err != nil {
		return false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	campaign, exists := repository.applicationCampaigns[item.CampaignID]
	if !exists {
		return false, errors.New("application campaign not found")
	}
	if item.RouteIndex >= len(campaign.Routes) {
		return false, errors.New("campaign application route is out of bounds")
	}
	if _, exists := repository.applicationsByID(item.ApplicationID); !exists {
		return false, errors.New("campaign application target not found")
	}
	key := campaignApplicationKey{campaignID: item.CampaignID, applicationID: item.ApplicationID}
	if _, exists := repository.campaignApplications[key]; exists {
		return false, nil
	}
	repository.campaignApplications[key] = item
	return true, nil
}

func (repository *Repository) ListCampaignApplications(ctx context.Context, id core.ApplicationCampaignID) ([]core.CampaignApplication, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errors.New("application campaign requires id")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if _, exists := repository.applicationCampaigns[id]; !exists {
		return nil, errors.New("application campaign not found")
	}
	items := make([]core.CampaignApplication, 0)
	for key, item := range repository.campaignApplications {
		if key.campaignID == id {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].RouteIndex != items[j].RouteIndex {
			return items[i].RouteIndex < items[j].RouteIndex
		}
		if !items[i].DiscoveredAt.Equal(items[j].DiscoveredAt) {
			return items[i].DiscoveredAt.Before(items[j].DiscoveredAt)
		}
		return items[i].ApplicationID < items[j].ApplicationID
	})
	return items, nil
}

func (repository *Repository) ListCampaignApplicationStates(ctx context.Context, id core.ApplicationCampaignID) ([]core.CampaignApplicationState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errors.New("application campaign requires id")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if _, exists := repository.applicationCampaigns[id]; !exists {
		return nil, errors.New("application campaign not found")
	}
	states := make([]core.CampaignApplicationState, 0)
	for key, item := range repository.campaignApplications {
		if key.campaignID != id {
			continue
		}
		application, exists := repository.applicationsByID(item.ApplicationID)
		if !exists {
			for _, tombstone := range repository.applicationTombstones {
				if tombstone.ApplicationID == item.ApplicationID {
					application = core.Application{ID: tombstone.ApplicationID, Key: tombstone.Key, Status: tombstone.Status}
					exists = true
					break
				}
			}
			if !exists {
				return nil, errors.New("campaign application target not found")
			}
		}
		states = append(states, core.CampaignApplicationState{Link: item, Application: application})
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].Link.RouteIndex != states[j].Link.RouteIndex {
			return states[i].Link.RouteIndex < states[j].Link.RouteIndex
		}
		leftFreshness := repository.applicationVacancyFreshness(states[i].Application)
		rightFreshness := repository.applicationVacancyFreshness(states[j].Application)
		if !leftFreshness.Equal(rightFreshness) {
			return leftFreshness.After(rightFreshness)
		}
		if !states[i].Link.DiscoveredAt.Equal(states[j].Link.DiscoveredAt) {
			return states[i].Link.DiscoveredAt.After(states[j].Link.DiscoveredAt)
		}
		return states[i].Link.ApplicationID < states[j].Link.ApplicationID
	})
	return states, nil
}

func (repository *Repository) applicationVacancyFreshness(application core.Application) time.Time {
	vacancy, exists := repository.vacancies[application.Key.Vacancy]
	if !exists {
		return time.Time{}
	}
	if vacancy.PublishedAt != nil {
		return *vacancy.PublishedAt
	}
	return vacancy.ObservedAt
}

func (repository *Repository) applicationsByID(id core.ApplicationID) (core.Application, bool) {
	for _, application := range repository.applications {
		if application.ID == id {
			return application, true
		}
	}
	return core.Application{}, false
}

func sameCampaignDefinition(left, right core.ApplicationCampaign) bool {
	return left.ID == right.ID && left.JobTag == right.JobTag &&
		slices.Equal(left.Profiles, right.Profiles) && slices.Equal(left.Routes, right.Routes) &&
		left.TargetSuccessful == right.TargetSuccessful && left.MaxInFlight == right.MaxInFlight &&
		left.CorrelationID == right.CorrelationID
}

func cloneApplicationCampaign(campaign core.ApplicationCampaign) core.ApplicationCampaign {
	campaign.Profiles = slices.Clone(campaign.Profiles)
	campaign.Routes = slices.Clone(campaign.Routes)
	return campaign
}
