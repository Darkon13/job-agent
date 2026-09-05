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

const maximumSuitableResumePages = 20

var _ adapter.SuitableResumeReader = (*ReadClient)(nil)

type suitableResumesResponse struct {
	Items []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"items"`
	Page  int `json:"page"`
	Pages int `json:"pages"`
}

func (client *ReadClient) ListSuitableResumes(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) ([]adapter.SuitableResume, error) {
	if profileID == "" || profileID != client.profileID {
		return nil, errors.New("HH suitable resume reader profile does not match")
	}
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if key.Platform != Name {
		return nil, errors.New("HH suitable resume reader requires an HH vacancy")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-client.profileLock:
	}
	defer func() { client.profileLock <- struct{}{} }()

	credentials, err := loadOAuthCredentials(client.credentialsRef)
	if err != nil {
		return nil, err
	}
	baseEndpoint := strings.TrimRight(client.apiBaseURL, "/") + "/vacancies/" + url.PathEscape(key.ExternalID) + "/suitable_resumes"
	resumes := make([]adapter.SuitableResume, 0)
	seen := make(map[string]struct{})
	for page := 0; page < maximumSuitableResumePages; page++ {
		payload, err := client.readSuitableResumePage(ctx, credentials.AccessToken, baseEndpoint, page)
		if err != nil {
			return nil, err
		}
		for _, item := range payload.Items {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				return nil, operationError(core.ErrorPermanentFailure, "vacancies.suitable_resumes", "HH returned a suitable resume without an id", nil)
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			resumes = append(resumes, adapter.SuitableResume{ID: id, Title: strings.TrimSpace(item.Title)})
		}
		pages := payload.Pages
		if pages <= 0 {
			pages = 1
		}
		if page+1 >= pages {
			return resumes, nil
		}
	}
	return nil, operationError(core.ErrorPermanentFailure, "vacancies.suitable_resumes", "HH suitable resume response exceeded the page safety limit", nil)
}

func (client *ReadClient) readSuitableResumePage(ctx context.Context, accessToken, endpoint string, page int) (suitableResumesResponse, error) {
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return suitableResumesResponse{}, fmt.Errorf("parse HH suitable resumes endpoint: %w", err)
	}
	query := requestURL.Query()
	query.Set("page", strconv.Itoa(page))
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return suitableResumesResponse{}, fmt.Errorf("create HH suitable resumes request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("HH-User-Agent", client.userAgent)

	httpClient := *client.httpClient
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return suitableResumesResponse{}, ctxErr
		}
		return suitableResumesResponse{}, operationError(core.ErrorTemporaryFailure, "vacancies.suitable_resumes", "HH suitable resume request failed", err)
	}
	defer response.Body.Close()
	if err := classifySuitableResumeResponse(response); err != nil {
		return suitableResumesResponse{}, err
	}
	var payload suitableResumesResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxSearchAPIResponse)).Decode(&payload); err != nil {
		return suitableResumesResponse{}, operationError(core.ErrorTemporaryFailure, "vacancies.suitable_resumes", "HH returned an invalid suitable resume response", err)
	}
	return payload, nil
}

func classifySuitableResumeResponse(response *http.Response) error {
	operation := "vacancies.suitable_resumes"
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		drain(response.Body)
		return operationError(core.ErrorUnauthorized, operation, "HH credentials cannot read suitable resumes", nil)
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
		drain(response.Body)
		return operationError(core.ErrorPermanentFailure, operation, "HH vacancy is unavailable", nil)
	case response.StatusCode == http.StatusTooManyRequests:
		drain(response.Body)
		failure := operationError(core.ErrorRateLimited, operation, "HH suitable resume read was rate limited", nil)
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return failure
	case response.StatusCode >= 500:
		drain(response.Body)
		return operationError(core.ErrorTemporaryFailure, operation, fmt.Sprintf("HH returned status %d", response.StatusCode), nil)
	default:
		drain(response.Body)
		return operationError(core.ErrorPermanentFailure, operation, fmt.Sprintf("HH rejected suitable resume read with status %d", response.StatusCode), nil)
	}
}
