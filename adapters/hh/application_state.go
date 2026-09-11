package hh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const (
	applicationStatePageSize = 100
	applicationStateMaxPages = 100
	maxNegotiationsResponse  = 16 << 20
)

var _ adapter.ApplicationStateObserver = (*ReadClient)(nil)

type hhNegotiationList struct {
	Items []struct {
		ID    string `json:"id"`
		State struct {
			ID string `json:"id"`
		} `json:"state"`
		UpdatedAt        string `json:"updated_at"`
		ViewedByOpponent *bool  `json:"viewed_by_opponent"`
		Vacancy          struct {
			ID string `json:"id"`
		} `json:"vacancy"`
	} `json:"items"`
	Pages *int `json:"pages"`
	Page  *int `json:"page"`
}

func (client *ReadClient) ObserveApplicationStates(ctx context.Context, profileID core.ProfileID) (adapter.ApplicationStateObservationResult, error) {
	if profileID == "" || profileID != client.profileID {
		return adapter.ApplicationStateObservationResult{}, errors.New("HH application observation profile does not match")
	}
	select {
	case <-ctx.Done():
		return adapter.ApplicationStateObservationResult{}, ctx.Err()
	case <-client.profileLock:
	}
	defer func() { client.profileLock <- struct{}{} }()
	credentials, err := loadOAuthCredentials(client.credentialsRef)
	if err != nil {
		return adapter.ApplicationStateObservationResult{}, err
	}
	observedAt := time.Now().UTC()
	result := make([]adapter.ApplicationStateObservation, 0)
	seen := make(map[string]struct{})
	for page := 0; page < applicationStateMaxPages; page++ {
		parameters := url.Values{
			"status": {"all"}, "page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(applicationStatePageSize)},
		}
		endpoint := strings.TrimRight(client.apiBaseURL, "/") + "/negotiations?" + parameters.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return adapter.ApplicationStateObservationResult{}, fmt.Errorf("create HH application observation request: %w", err)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
		request.Header.Set("HH-User-Agent", client.userAgent)
		response, err := client.httpClient.Do(request)
		if err != nil {
			return adapter.ApplicationStateObservationResult{}, operationError(core.ErrorTemporaryFailure, "applications.observe", "HH negotiation list request failed", err)
		}
		var payload hhNegotiationList
		if response.StatusCode == http.StatusOK {
			err = json.NewDecoder(io.LimitReader(response.Body, maxNegotiationsResponse)).Decode(&payload)
			_ = response.Body.Close()
			if err != nil {
				return adapter.ApplicationStateObservationResult{}, operationError(core.ErrorTemporaryFailure, "applications.observe", "HH returned an invalid negotiation list", err)
			}
		} else {
			failure := readHHError(response.Body)
			_ = response.Body.Close()
			return adapter.ApplicationStateObservationResult{}, classifyApplicationObservationFailure(response, failure)
		}
		if payload.Page == nil || payload.Pages == nil || *payload.Page != page || *payload.Pages < 0 ||
			(*payload.Pages == 0 && (page != 0 || len(payload.Items) != 0)) ||
			(*payload.Pages > 0 && page >= *payload.Pages) || (len(payload.Items) == 0 && page+1 < *payload.Pages) {
			return adapter.ApplicationStateObservationResult{}, operationError(core.ErrorTemporaryFailure, "applications.observe", "HH returned invalid negotiation pagination", nil)
		}
		for _, item := range payload.Items {
			negotiationID := strings.TrimSpace(item.ID)
			vacancyID := strings.TrimSpace(item.Vacancy.ID)
			platformState := strings.TrimSpace(item.State.ID)
			if negotiationID == "" || vacancyID == "" || platformState == "" {
				return adapter.ApplicationStateObservationResult{}, operationError(core.ErrorTemporaryFailure, "applications.observe", "HH returned an incomplete negotiation", nil)
			}
			if _, duplicate := seen[negotiationID]; duplicate {
				return adapter.ApplicationStateObservationResult{}, operationError(core.ErrorTemporaryFailure, "applications.observe", "HH negotiation pagination changed; retry observation", nil)
			}
			seen[negotiationID] = struct{}{}
			updatedAt, err := parseHHTime(item.UpdatedAt)
			if err != nil {
				return adapter.ApplicationStateObservationResult{}, operationError(core.ErrorTemporaryFailure, "applications.observe", "HH returned an invalid negotiation update time", err)
			}
			result = append(result, adapter.ApplicationStateObservation{
				ExternalNegotiationID: negotiationID, ExternalVacancyID: vacancyID,
				PlatformState: platformState, Disposition: hhApplicationDisposition(platformState),
				ViewedByOpponent: item.ViewedByOpponent, PlatformUpdatedAt: updatedAt,
			})
		}
		if *payload.Pages == 0 || page+1 >= *payload.Pages {
			return adapter.ApplicationStateObservationResult{Applications: result, ObservedAt: observedAt}, nil
		}
	}
	return adapter.ApplicationStateObservationResult{}, operationError(core.ErrorTemporaryFailure, "applications.observe", "HH negotiation pagination exceeded its safety limit", nil)
}

func hhApplicationDisposition(state string) core.ApplicationDisposition {
	switch state {
	case "response":
		return core.ApplicationDispositionPending
	case "invitation":
		return core.ApplicationDispositionInvited
	case "discard":
		return core.ApplicationDispositionRejected
	case "hidden":
		return core.ApplicationDispositionHidden
	default:
		return core.ApplicationDispositionUnknown
	}
}

func classifyApplicationObservationFailure(response *http.Response, failure hhErrorResponse) error {
	for _, item := range failure.Errors {
		if item.Type == "oauth" {
			return operationError(core.ErrorUnauthorized, "applications.observe", "HH credentials are expired, revoked or invalid", nil)
		}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return operationError(core.ErrorUnauthorized, "applications.observe", "HH application observation is unauthorized", nil)
	}
	if response.StatusCode == http.StatusTooManyRequests {
		err := operationError(core.ErrorRateLimited, "applications.observe", "HH application observation was rate limited", nil)
		err.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return err
	}
	category := core.ErrorPermanentFailure
	if response.StatusCode >= 500 {
		category = core.ErrorTemporaryFailure
	}
	return operationError(category, "applications.observe", fmt.Sprintf("HH returned status %d", response.StatusCode), nil)
}
