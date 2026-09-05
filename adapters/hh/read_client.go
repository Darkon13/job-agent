package hh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const (
	defaultAPIBaseURL = "https://api.hh.ru"
	defaultUserAgent  = "job-agent/0.1 (local installation)"
	maxAPIResponse    = 1 << 20
)

// ReadClient owns the authenticated read transport of one HH profile. The
// semaphore serializes session checks now and token refresh later, while still
// allowing a caller waiting for the profile lock to cancel its context.
type ReadClient struct {
	profileID      core.ProfileID
	credentialsRef string
	apiBaseURL     string
	userAgent      string
	httpClient     *http.Client
	profileLock    chan struct{}
}

type oauthCredentials struct {
	AccessToken string `json:"access_token"`
}

type meResponse struct {
	ID          string `json:"id"`
	AuthType    string `json:"auth_type"`
	IsApplicant bool   `json:"is_applicant"`
}

var _ adapter.ProfileReader = (*ReadClient)(nil)

func NewReadClient(profileID core.ProfileID, credentialsRef, userAgent string, client *http.Client) (*ReadClient, error) {
	if profileID == "" {
		return nil, errors.New("HH read client requires profile")
	}
	if strings.TrimSpace(credentialsRef) == "" {
		return nil, errors.New("HH read client requires credentials_ref")
	}
	if strings.TrimSpace(userAgent) == "" {
		userAgent = defaultUserAgent
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	lock := make(chan struct{}, 1)
	lock <- struct{}{}
	return &ReadClient{
		profileID: profileID, credentialsRef: credentialsRef,
		apiBaseURL: defaultAPIBaseURL, userAgent: userAgent,
		httpClient: client, profileLock: lock,
	}, nil
}

func (client *ReadClient) ReadProfile(ctx context.Context, profileID core.ProfileID) (adapter.ProfileReadResult, error) {
	if profileID == "" || profileID != client.profileID {
		return adapter.ProfileReadResult{}, errors.New("HH read client profile does not match")
	}
	select {
	case <-ctx.Done():
		return adapter.ProfileReadResult{}, ctx.Err()
	case <-client.profileLock:
	}
	defer func() { client.profileLock <- struct{}{} }()

	credentials, err := loadOAuthCredentials(client.credentialsRef)
	if err != nil {
		return adapter.ProfileReadResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(client.apiBaseURL, "/")+"/me", nil)
	if err != nil {
		return adapter.ProfileReadResult{}, fmt.Errorf("create HH profile request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	request.Header.Set("HH-User-Agent", client.userAgent)

	httpClient := *client.httpClient
	if httpClient.CheckRedirect == nil {
		httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return adapter.ProfileReadResult{}, ctxErr
		}
		return adapter.ProfileReadResult{}, operationError(core.ErrorTemporaryFailure, "profiles.read", "HH profile request failed", err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		drain(response.Body)
		return adapter.ProfileReadResult{}, operationError(core.ErrorUnauthorized, "profiles.read", "HH credentials are expired, revoked or invalid", nil)
	case response.StatusCode == http.StatusTooManyRequests:
		drain(response.Body)
		failure := operationError(core.ErrorRateLimited, "profiles.read", "HH profile check was rate limited", nil)
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return adapter.ProfileReadResult{}, failure
	case response.StatusCode >= 500:
		drain(response.Body)
		return adapter.ProfileReadResult{}, operationError(core.ErrorTemporaryFailure, "profiles.read", fmt.Sprintf("HH returned status %d", response.StatusCode), nil)
	case response.StatusCode < 200 || response.StatusCode >= 300:
		drain(response.Body)
		return adapter.ProfileReadResult{}, operationError(core.ErrorPermanentFailure, "profiles.read", fmt.Sprintf("HH rejected profile check with status %d", response.StatusCode), nil)
	}

	var current meResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxAPIResponse))
	if err := decoder.Decode(&current); err != nil {
		return adapter.ProfileReadResult{}, operationError(core.ErrorTemporaryFailure, "profiles.read", "HH returned an invalid profile response", err)
	}
	if strings.TrimSpace(current.ID) == "" {
		return adapter.ProfileReadResult{}, operationError(core.ErrorPermanentFailure, "profiles.read", "HH profile response has no account id", nil)
	}
	if current.AuthType != "applicant" && !current.IsApplicant {
		return adapter.ProfileReadResult{}, operationError(core.ErrorPermanentFailure, "profiles.read", "HH credentials do not belong to an applicant", nil)
	}
	return adapter.ProfileReadResult{ExternalAccountID: current.ID, AuthType: current.AuthType}, nil
}

func loadOAuthCredentials(reference string) (oauthCredentials, error) {
	path := strings.TrimSpace(reference)
	if strings.HasPrefix(path, "file:") {
		path = strings.TrimPrefix(path, "file:")
	}
	if path == "" || strings.Contains(path, "://") {
		return oauthCredentials{}, operationError(core.ErrorUnauthorized, "profiles.read.auth", "unsupported credentials_ref", nil)
	}
	file, err := os.Open(path)
	if err != nil {
		return oauthCredentials{}, operationError(core.ErrorUnauthorized, "profiles.read.auth", "credential file is unavailable", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return oauthCredentials{}, operationError(core.ErrorUnauthorized, "profiles.read.auth", "credential file is unavailable", err)
	}
	if !info.Mode().IsRegular() {
		return oauthCredentials{}, operationError(core.ErrorUnauthorized, "profiles.read.auth", "credential reference is not a regular file", nil)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return oauthCredentials{}, operationError(core.ErrorUnauthorized, "profiles.read.auth", "credential file permissions must not allow group or other access", nil)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAPIResponse))
	if err != nil {
		return oauthCredentials{}, operationError(core.ErrorUnauthorized, "profiles.read.auth", "credential file is unavailable", err)
	}
	var credentials oauthCredentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		return oauthCredentials{}, operationError(core.ErrorUnauthorized, "profiles.read.auth", "credential file is invalid", err)
	}
	if strings.TrimSpace(credentials.AccessToken) == "" {
		return oauthCredentials{}, operationError(core.ErrorUnauthorized, "profiles.read.auth", "credential file has no access token", nil)
	}
	return credentials, nil
}

func operationError(category core.ErrorCategory, operation, message string, cause error) *core.OperationError {
	return &core.OperationError{Category: category, Operation: operation, Platform: Name, Message: message, Cause: cause}
}

func drain(reader io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(reader, maxAPIResponse))
}

func retryAfter(value string, now time.Time) *time.Time {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		result := now.Add(time.Duration(seconds) * time.Second)
		return &result
	}
	if parsed, err := http.ParseTime(value); err == nil {
		result := parsed.UTC()
		return &result
	}
	return nil
}
