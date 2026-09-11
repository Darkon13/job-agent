package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func (store *Store) CreateApplicationCampaign(ctx context.Context, candidate core.ApplicationCampaign) (core.ApplicationCampaign, bool, error) {
	if err := candidate.Validate(); err != nil {
		return core.ApplicationCampaign{}, false, err
	}
	profiles, routes, err := encodeCampaignDefinition(candidate)
	if err != nil {
		return core.ApplicationCampaign{}, false, err
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO application_campaigns
		(campaign_id, job_tag, profiles, routes, target_successful, max_in_flight,
		 route_index, cursor, route_done, status, stop_reason, correlation_id,
		 revision, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidate.ID, candidate.JobTag, profiles, routes, candidate.TargetSuccessful, candidate.MaxInFlight,
		candidate.RouteIndex, candidate.Cursor, candidate.RouteDone, candidate.Status, candidate.StopReason,
		candidate.CorrelationID, candidate.Revision, candidate.CreatedAt.UnixNano(), candidate.UpdatedAt.UnixNano())
	if err != nil {
		return core.ApplicationCampaign{}, false, fmt.Errorf("insert application campaign %s: %w", candidate.ID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil {
		return core.ApplicationCampaign{}, false, err
	}
	stored, err := store.ApplicationCampaign(ctx, candidate.ID)
	if err != nil {
		return core.ApplicationCampaign{}, false, err
	}
	if !sameApplicationCampaignDefinition(stored, candidate) {
		return core.ApplicationCampaign{}, false, fmt.Errorf("application campaign %s conflicts with changed definition", candidate.ID)
	}
	return stored, created, nil
}

func (store *Store) ApplicationCampaign(ctx context.Context, id core.ApplicationCampaignID) (core.ApplicationCampaign, error) {
	if id == "" {
		return core.ApplicationCampaign{}, errors.New("application campaign requires id")
	}
	row := store.db.QueryRowContext(ctx, `SELECT job_tag, profiles, routes, target_successful,
		max_in_flight, route_index, cursor, route_done, status, stop_reason,
		correlation_id, revision, created_at, updated_at
		FROM application_campaigns WHERE campaign_id = ?`, id)
	var campaign core.ApplicationCampaign
	var profiles, routes []byte
	var createdAt, updatedAt int64
	campaign.ID = id
	if err := row.Scan(&campaign.JobTag, &profiles, &routes, &campaign.TargetSuccessful,
		&campaign.MaxInFlight, &campaign.RouteIndex, &campaign.Cursor, &campaign.RouteDone,
		&campaign.Status, &campaign.StopReason, &campaign.CorrelationID, &campaign.Revision,
		&createdAt, &updatedAt); err != nil {
		return core.ApplicationCampaign{}, err
	}
	if err := json.Unmarshal(profiles, &campaign.Profiles); err != nil {
		return core.ApplicationCampaign{}, fmt.Errorf("decode application campaign profiles: %w", err)
	}
	if err := json.Unmarshal(routes, &campaign.Routes); err != nil {
		return core.ApplicationCampaign{}, fmt.Errorf("decode application campaign routes: %w", err)
	}
	campaign.CreatedAt = time.Unix(0, createdAt).UTC()
	campaign.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if err := campaign.Validate(); err != nil {
		return core.ApplicationCampaign{}, fmt.Errorf("invalid stored application campaign %s: %w", id, err)
	}
	return campaign, nil
}

func (store *Store) ListApplicationCampaigns(ctx context.Context, limit int) ([]core.ApplicationCampaign, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("application campaign list limit must be between 1 and 100")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT campaign_id, job_tag, profiles, routes, target_successful,
		max_in_flight, route_index, cursor, route_done, status, stop_reason,
		correlation_id, revision, created_at, updated_at
		FROM application_campaigns ORDER BY updated_at DESC, campaign_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list application campaigns: %w", err)
	}
	defer rows.Close()
	campaigns := make([]core.ApplicationCampaign, 0, limit)
	for rows.Next() {
		var campaign core.ApplicationCampaign
		var profiles, routes []byte
		var createdAt, updatedAt int64
		if err := rows.Scan(&campaign.ID, &campaign.JobTag, &profiles, &routes, &campaign.TargetSuccessful,
			&campaign.MaxInFlight, &campaign.RouteIndex, &campaign.Cursor, &campaign.RouteDone,
			&campaign.Status, &campaign.StopReason, &campaign.CorrelationID, &campaign.Revision,
			&createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(profiles, &campaign.Profiles); err != nil {
			return nil, fmt.Errorf("decode application campaign %s profiles: %w", campaign.ID, err)
		}
		if err := json.Unmarshal(routes, &campaign.Routes); err != nil {
			return nil, fmt.Errorf("decode application campaign %s routes: %w", campaign.ID, err)
		}
		campaign.CreatedAt = time.Unix(0, createdAt).UTC()
		campaign.UpdatedAt = time.Unix(0, updatedAt).UTC()
		if err := campaign.Validate(); err != nil {
			return nil, fmt.Errorf("invalid stored application campaign %s: %w", campaign.ID, err)
		}
		campaigns = append(campaigns, campaign)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return campaigns, nil
}

func (store *Store) SaveApplicationCampaign(ctx context.Context, candidate core.ApplicationCampaign, expectedRevision uint64) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Revision != expectedRevision+1 {
		return errors.New("application campaign revision must advance by one")
	}
	stored, err := store.ApplicationCampaign(ctx, candidate.ID)
	if err != nil {
		return err
	}
	if !sameApplicationCampaignDefinition(stored, candidate) {
		return errors.New("application campaign immutable definition changed")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE application_campaigns SET
		route_index = ?, cursor = ?, route_done = ?, status = ?, stop_reason = ?,
		revision = ?, updated_at = ? WHERE campaign_id = ? AND revision = ?`,
		candidate.RouteIndex, candidate.Cursor, candidate.RouteDone, candidate.Status,
		candidate.StopReason, candidate.Revision, candidate.UpdatedAt.UnixNano(),
		candidate.ID, expectedRevision)
	if err != nil {
		return fmt.Errorf("save application campaign %s: %w", candidate.ID, err)
	}
	updated, err := oneRowAffected(result)
	if err != nil {
		return err
	}
	if !updated {
		return storage.ErrRevisionConflict
	}
	return nil
}

func (store *Store) LinkCampaignApplication(ctx context.Context, item core.CampaignApplication) (bool, error) {
	if err := item.Validate(); err != nil {
		return false, err
	}
	campaign, err := store.ApplicationCampaign(ctx, item.CampaignID)
	if err != nil {
		return false, err
	}
	if item.RouteIndex >= len(campaign.Routes) {
		return false, errors.New("campaign application route is out of bounds")
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO application_campaign_items
		(campaign_id, application_id, route_index, discovered_at) VALUES (?, ?, ?, ?)`,
		item.CampaignID, item.ApplicationID, item.RouteIndex, item.DiscoveredAt.UnixNano())
	if err != nil {
		return false, fmt.Errorf("link application %s to campaign %s: %w", item.ApplicationID, item.CampaignID, err)
	}
	return oneRowAffected(result)
}

func (store *Store) ListCampaignApplications(ctx context.Context, id core.ApplicationCampaignID) ([]core.CampaignApplication, error) {
	if id == "" {
		return nil, errors.New("application campaign requires id")
	}
	if _, err := store.ApplicationCampaign(ctx, id); err != nil {
		return nil, err
	}
	rows, err := store.db.QueryContext(ctx, `SELECT route_index, application_id, discovered_at
		FROM application_campaign_items WHERE campaign_id = ?
		ORDER BY route_index, discovered_at, application_id`, id)
	if err != nil {
		return nil, fmt.Errorf("list campaign applications %s: %w", id, err)
	}
	defer rows.Close()
	items := make([]core.CampaignApplication, 0)
	for rows.Next() {
		var item core.CampaignApplication
		var discoveredAt int64
		item.CampaignID = id
		if err := rows.Scan(&item.RouteIndex, &item.ApplicationID, &discoveredAt); err != nil {
			return nil, err
		}
		item.DiscoveredAt = time.Unix(0, discoveredAt).UTC()
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("invalid campaign application %s: %w", item.ApplicationID, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (store *Store) ListCampaignApplicationStates(ctx context.Context, id core.ApplicationCampaignID) ([]core.CampaignApplicationState, error) {
	if id == "" {
		return nil, errors.New("application campaign requires id")
	}
	if _, err := store.ApplicationCampaign(ctx, id); err != nil {
		return nil, err
	}
	rows, err := store.db.QueryContext(ctx, `WITH outcomes AS (
		SELECT id, profile_id, platform, external_id, status, attempts, external_negotiation_id,
			failure_category, failure_message, decision_code, decision_reason, prepared_resume_id,
			prepared_message, preparation_provenance, created_at, updated_at, prepared_at, submitted_at FROM applications
		UNION ALL
		SELECT application_id, profile_id, platform, external_id, status, 0, '', '', '', '', '', '', '', '{}',
			removed_at, removed_at, NULL, NULL FROM application_tombstones
	) SELECT
		i.route_index, i.application_id, i.discovered_at,
		a.profile_id, a.platform, a.external_id, a.status, a.attempts,
		a.external_negotiation_id, a.failure_category, a.failure_message,
		a.decision_code, a.decision_reason, a.prepared_resume_id, a.prepared_message,
		a.preparation_provenance, a.created_at, a.updated_at, a.prepared_at, a.submitted_at
		FROM application_campaign_items i
		JOIN outcomes a ON a.id = i.application_id
		JOIN vacancies v ON v.platform = a.platform AND v.external_id = a.external_id
		WHERE i.campaign_id = ?
		ORDER BY i.route_index,
			COALESCE(v.published_at, v.observed_at) DESC,
			v.observed_at DESC, i.discovered_at DESC, i.application_id`, id)
	if err != nil {
		return nil, fmt.Errorf("list campaign application states %s: %w", id, err)
	}
	defer rows.Close()
	states := make([]core.CampaignApplicationState, 0)
	for rows.Next() {
		var state core.CampaignApplicationState
		var discoveredAt, createdAt, updatedAt int64
		var preparedAt, submittedAt sql.NullInt64
		var preparationProvenance []byte
		state.Link.CampaignID = id
		if err := rows.Scan(
			&state.Link.RouteIndex, &state.Link.ApplicationID, &discoveredAt,
			&state.Application.Key.ProfileID, &state.Application.Key.Vacancy.Platform,
			&state.Application.Key.Vacancy.ExternalID, &state.Application.Status, &state.Application.Attempts,
			&state.Application.ExternalNegotiationID, &state.Application.FailureCategory, &state.Application.FailureMessage,
			&state.Application.DecisionCode, &state.Application.DecisionReason, &state.Application.PreparedResumeID,
			&state.Application.PreparedMessage, &preparationProvenance, &createdAt, &updatedAt, &preparedAt, &submittedAt,
		); err != nil {
			return nil, err
		}
		if err := unmarshalApplicationPreparationProvenance(preparationProvenance, &state.Application.PreparationProvenance); err != nil {
			return nil, fmt.Errorf("decode campaign application preparation provenance: %w", err)
		}
		state.Link.DiscoveredAt = time.Unix(0, discoveredAt).UTC()
		state.Application.ID = state.Link.ApplicationID
		state.Application.CreatedAt = time.Unix(0, createdAt).UTC()
		state.Application.UpdatedAt = time.Unix(0, updatedAt).UTC()
		state.Application.PreparedAt = timeFromNull(preparedAt)
		state.Application.SubmittedAt = timeFromNull(submittedAt)
		if err := state.Link.Validate(); err != nil {
			return nil, fmt.Errorf("invalid campaign application %s: %w", state.Link.ApplicationID, err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return states, nil
}

func encodeCampaignDefinition(campaign core.ApplicationCampaign) ([]byte, []byte, error) {
	profiles, err := json.Marshal(campaign.Profiles)
	if err != nil {
		return nil, nil, fmt.Errorf("encode application campaign profiles: %w", err)
	}
	routes, err := json.Marshal(campaign.Routes)
	if err != nil {
		return nil, nil, fmt.Errorf("encode application campaign routes: %w", err)
	}
	return profiles, routes, nil
}

func sameApplicationCampaignDefinition(left, right core.ApplicationCampaign) bool {
	leftProfiles, leftRoutes, leftErr := encodeCampaignDefinition(left)
	rightProfiles, rightRoutes, rightErr := encodeCampaignDefinition(right)
	return leftErr == nil && rightErr == nil && left.ID == right.ID && left.JobTag == right.JobTag &&
		bytes.Equal(leftProfiles, rightProfiles) && bytes.Equal(leftRoutes, rightRoutes) &&
		left.TargetSuccessful == right.TargetSuccessful && left.MaxInFlight == right.MaxInFlight &&
		left.CorrelationID == right.CorrelationID
}
