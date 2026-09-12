package hh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

var _ adapter.VacancyReader = (*ReadClient)(nil)

func (client *ReadClient) ReadVacancy(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) (core.Vacancy, error) {
	if profileID == "" || profileID != client.profileID {
		return core.Vacancy{}, errors.New("HH vacancy reader profile does not match")
	}
	if err := key.Validate(); err != nil {
		return core.Vacancy{}, err
	}
	if key.Platform != Name {
		return core.Vacancy{}, errors.New("HH vacancy reader requires an HH vacancy")
	}
	select {
	case <-ctx.Done():
		return core.Vacancy{}, ctx.Err()
	case <-client.profileLock:
	}
	defer func() { client.profileLock <- struct{}{} }()

	credentials, err := loadOAuthCredentials(client.credentialsRef)
	if err != nil {
		return core.Vacancy{}, err
	}
	endpoint := strings.TrimRight(client.apiBaseURL, "/") + "/vacancies/" + url.PathEscape(key.ExternalID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return core.Vacancy{}, fmt.Errorf("create HH vacancy request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	request.Header.Set("HH-User-Agent", client.userAgent)

	httpClient := *client.httpClient
	if httpClient.CheckRedirect == nil {
		httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	}
	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return core.Vacancy{}, ctxErr
		}
		return core.Vacancy{}, operationError(core.ErrorTemporaryFailure, "vacancies.read", "HH vacancy request failed", err)
	}
	defer response.Body.Close()
	if err := classifyVacancyResponse(response); err != nil {
		return core.Vacancy{}, err
	}

	var item vacancySearchItem
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxSearchAPIResponse))
	if err := decoder.Decode(&item); err != nil {
		return core.Vacancy{}, operationError(core.ErrorTemporaryFailure, "vacancies.read", "HH returned an invalid vacancy response", err)
	}
	if item.ID != key.ExternalID {
		return core.Vacancy{}, operationError(core.ErrorPermanentFailure, "vacancies.read", "HH returned another vacancy", nil)
	}
	vacancy, err := normalizeSearchItem(item, time.Now().UTC())
	if err != nil {
		return core.Vacancy{}, operationError(core.ErrorPermanentFailure, "vacancies.read", "HH returned an invalid vacancy", err)
	}
	return vacancy, nil
}

func classifyVacancyResponse(response *http.Response) error {
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusUnauthorized:
		drain(response.Body)
		return operationError(core.ErrorUnauthorized, "vacancies.read", "HH credentials are expired, revoked or invalid", nil)
	case response.StatusCode == http.StatusForbidden:
		// HH answers 403 for a specific vacancy that the account may not see
		// (region or employer restrictions) while the session itself is fine.
		drain(response.Body)
		return operationError(core.ErrorPermanentFailure, "vacancies.read", "HH vacancy is not accessible for this account", nil)
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
		drain(response.Body)
		return operationError(core.ErrorPermanentFailure, "vacancies.read", "HH vacancy is unavailable", nil)
	case response.StatusCode == http.StatusTooManyRequests:
		drain(response.Body)
		failure := operationError(core.ErrorRateLimited, "vacancies.read", "HH vacancy read was rate limited", nil)
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return failure
	case response.StatusCode >= 500:
		drain(response.Body)
		return operationError(core.ErrorTemporaryFailure, "vacancies.read", fmt.Sprintf("HH returned status %d", response.StatusCode), nil)
	default:
		drain(response.Body)
		return operationError(core.ErrorPermanentFailure, "vacancies.read", fmt.Sprintf("HH rejected vacancy read with status %d", response.StatusCode), nil)
	}
}
