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

const maxResumePublishResponse = 64 << 10

type resumePublishResponse struct {
	NextPublishAt string `json:"next_publish_at"`
}

// PublishResume publishes one resume through the applicant API. HH answers 204
// on success; an early publish attempt carries next_publish_at in the error
// body and is normalized into a rate limit with RetryAfter.
func (client *ReadClient) PublishResume(ctx context.Context, command adapter.ResumePublishCommand) (adapter.ResumePublishResult, error) {
	if strings.TrimSpace(command.ResumeID) == "" {
		return adapter.ResumePublishResult{}, errors.New("HH resume publish requires resume id")
	}
	if command.ProfileID != "" && command.ProfileID != client.profileID {
		return adapter.ResumePublishResult{}, errors.New("HH resume publish profile does not match")
	}
	select {
	case <-ctx.Done():
		return adapter.ResumePublishResult{}, ctx.Err()
	case <-client.profileLock:
	}
	defer func() { client.profileLock <- struct{}{} }()

	credentials, err := loadOAuthCredentials(client.credentialsRef)
	if err != nil {
		return adapter.ResumePublishResult{}, err
	}
	endpoint := strings.TrimRight(client.apiBaseURL, "/") + "/resumes/" +
		url.PathEscape(strings.TrimSpace(command.ResumeID)) + "/publish"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return adapter.ResumePublishResult{}, fmt.Errorf("create HH resume publish request: %w", err)
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
			return adapter.ResumePublishResult{}, ctxErr
		}
		return adapter.ResumePublishResult{}, operationError(core.ErrorTemporaryFailure, "resumes.publish", "HH resume publish request failed", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxResumePublishResponse))

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return adapter.ResumePublishResult{NextPublishAt: parseResumePublishNext(body)}, nil
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return adapter.ResumePublishResult{}, operationError(core.ErrorUnauthorized, "resumes.publish", "HH credentials are expired, revoked or invalid", nil)
	case response.StatusCode == http.StatusTooManyRequests:
		failure := operationError(core.ErrorRateLimited, "resumes.publish", "HH resume publish was rate limited", nil)
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		if failure.RetryAfter == nil {
			failure.RetryAfter = parseResumePublishNext(body)
		}
		return adapter.ResumePublishResult{}, failure
	case response.StatusCode == http.StatusBadRequest:
		if next := parseResumePublishNext(body); next != nil {
			failure := operationError(core.ErrorRateLimited, "resumes.publish", "HH resume cannot be published yet", nil)
			failure.RetryAfter = next
			return adapter.ResumePublishResult{}, failure
		}
		return adapter.ResumePublishResult{}, operationError(core.ErrorPermanentFailure, "resumes.publish", "HH rejected resume publish", nil)
	case response.StatusCode >= 500:
		return adapter.ResumePublishResult{}, operationError(core.ErrorTemporaryFailure, "resumes.publish", fmt.Sprintf("HH returned status %d", response.StatusCode), nil)
	default:
		return adapter.ResumePublishResult{}, operationError(core.ErrorPermanentFailure, "resumes.publish", fmt.Sprintf("HH rejected resume publish with status %d", response.StatusCode), nil)
	}
}

func parseResumePublishNext(body []byte) *time.Time {
	if len(body) == 0 {
		return nil
	}
	var parsed resumePublishResponse
	if err := json.Unmarshal(body, &parsed); err != nil || strings.TrimSpace(parsed.NextPublishAt) == "" {
		return nil
	}
	next, err := parseHHTime(parsed.NextPublishAt)
	if err != nil {
		return nil
	}
	return next
}
