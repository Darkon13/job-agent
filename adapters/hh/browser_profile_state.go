package hh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const maxBrowserProfileStateResponse = 4 << 20

// BrowserProfileStateClient is an explicitly bound write transport. Each
// attempt performs read/compare, narrowly scoped writes, and a final read-back.
type BrowserProfileStateClient struct {
	reader     *BrowserReadClient
	webBaseURL string
	mu         sync.Mutex
}

var _ adapter.ProfileStateWriter = (*BrowserProfileStateClient)(nil)

func newBrowserProfileStateClient(reader *BrowserReadClient) *BrowserProfileStateClient {
	return &BrowserProfileStateClient{reader: reader, webBaseURL: reader.webBaseURL}
}

func (client *BrowserProfileStateClient) ApplyProfileState(ctx context.Context, proposal core.ProfileStateProposal) (adapter.ProfileStateApplyResult, error) {
	if err := proposal.Validate(); err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	if proposal.ProfileID != client.reader.profileID {
		return adapter.ProfileStateApplyResult{}, errors.New("HH browser profile state writer profile does not match")
	}
	paths, err := proposal.DeclaredPaths()
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	observation, err := client.reader.ReadProfileState(ctx, adapter.ProfileStateReadRequest{ProfileID: proposal.ProfileID, Paths: paths})
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	pending, err := proposal.ChangesToApply(observation)
	if err != nil {
		return adapter.ProfileStateApplyResult{}, profileStateConflict(err)
	}
	if len(pending) == 0 {
		return adapter.ProfileStateApplyResult{Observation: observation, AlreadyApplied: true}, nil
	}
	values, err := desiredHHAboutValues(proposal.DesiredState, pending)
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	for _, change := range pending {
		resumeID, _ := hhAboutResumeID(change.Path)
		if err := client.postAbout(ctx, resumeID, values[resumeID]); err != nil {
			if core.ErrorIsCategory(err, core.ErrorAmbiguousResult) {
				if reconciled, reconcileErr := client.readBack(ctx, proposal, paths); reconcileErr == nil && len(reconciled.pending) == 0 {
					return adapter.ProfileStateApplyResult{Observation: reconciled.observation}, nil
				}
			}
			return adapter.ProfileStateApplyResult{}, err
		}
	}
	result, err := client.readBack(ctx, proposal, paths)
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	if len(result.pending) != 0 {
		return adapter.ProfileStateApplyResult{}, operationError(core.ErrorAmbiguousResult, "profile_state.apply.browser", "HH accepted the update but read-back did not confirm every field", nil)
	}
	return adapter.ProfileStateApplyResult{Observation: result.observation}, nil
}

type profileStateReadBack struct {
	observation core.ProfileStateObservation
	pending     []core.ProfileStateChange
}

func (client *BrowserProfileStateClient) readBack(ctx context.Context, proposal core.ProfileStateProposal, paths []string) (profileStateReadBack, error) {
	observation, err := client.reader.ReadProfileState(ctx, adapter.ProfileStateReadRequest{ProfileID: proposal.ProfileID, Paths: paths})
	if err != nil {
		return profileStateReadBack{}, err
	}
	pending, err := proposal.ChangesToApply(observation)
	if err != nil {
		return profileStateReadBack{}, profileStateConflict(err)
	}
	return profileStateReadBack{observation: observation, pending: pending}, nil
}

func (client *BrowserProfileStateClient) postAbout(ctx context.Context, resumeID string, about *string) error {
	const operation = "profile_state.apply.browser"
	endpoint, _ := url.Parse(strings.TrimRight(client.webBaseURL, "/") + "/applicant/resume/edit")
	query := endpoint.Query()
	query.Set("resume", resumeID)
	query.Set("hhtmSource", "profile-state")
	endpoint.RawQuery = query.Encode()
	skills := make([]string, 0, 1)
	if about != nil {
		skills = append(skills, *about)
	}
	body, err := json.Marshal(map[string]any{"skills": skills})
	if err != nil {
		return fmt.Errorf("encode HH about update: %w", err)
	}
	httpClient, err := client.authenticatedClient(endpoint.String())
	if err != nil {
		return err
	}
	copy := *httpClient
	copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create HH about update request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", client.reader.userAgent)
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	request.Header.Set("Referer", strings.TrimRight(client.webBaseURL, "/")+"/resume/edit/"+url.PathEscape(resumeID)+"/about")
	if xsrf := cookieValue(httpClient, endpoint, "_xsrf"); xsrf != "" {
		request.Header.Set("X-Xsrftoken", xsrf)
	}
	response, err := copy.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return operationError(core.ErrorAmbiguousResult, operation, "HH profile update outcome is unknown after transport failure", err)
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxBrowserProfileStateResponse))
	if readErr != nil {
		return operationError(core.ErrorAmbiguousResult, operation, "HH profile update outcome is unknown after reading the response", readErr)
	}
	return classifyBrowserProfileStatePOST(response, data)
}

func (client *BrowserProfileStateClient) authenticatedClient(endpoint string) (*http.Client, error) {
	transport, err := NewResumeTouchTransport(client.reader.stateFile, client.reader.httpClient)
	if err != nil {
		return nil, err
	}
	transport.profileURL = endpoint
	transport.touchURL = endpoint
	return transport.authenticatedClient()
}

func desiredHHAboutValues(state json.RawMessage, pending []core.ProfileStateChange) (map[string]*string, error) {
	var desired struct {
		Resumes map[string]map[string]json.RawMessage `json:"resumes"`
	}
	if err := json.Unmarshal(state, &desired); err != nil {
		return nil, fmt.Errorf("decode HH desired profile state: %w", err)
	}
	values := make(map[string]*string, len(pending))
	for _, change := range pending {
		resumeID, supported := hhAboutResumeID(change.Path)
		if !supported {
			return nil, operationError(core.ErrorUnsupported, "profile_state.apply.browser", "HH browser writer supports only resume about fields", nil)
		}
		raw, exists := desired.Resumes[resumeID]["about"]
		if !exists {
			return nil, errors.New("HH desired profile state has no declared about value")
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			values[resumeID] = nil
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, operationError(core.ErrorValidationRequired, "profile_state.apply.browser", "HH resume about must be a string or null", err)
		}
		if value == "" {
			return nil, operationError(core.ErrorValidationRequired, "profile_state.apply.browser", "use null to clear HH resume about", nil)
		}
		copy := value
		values[resumeID] = &copy
	}
	return values, nil
}

func classifyBrowserProfileStatePOST(response *http.Response, data []byte) error {
	const operation = "profile_state.apply.browser"
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		var payload map[string]json.RawMessage
		if len(bytes.TrimSpace(data)) != 0 && json.Unmarshal(data, &payload) == nil {
			if raw := payload["errors"]; len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && !bytes.Equal(bytes.TrimSpace(raw), []byte("[]")) && !bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) {
				return operationError(core.ErrorValidationRequired, operation, "HH rejected one or more resume fields", nil)
			}
		}
		return nil
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return operationError(core.ErrorUnauthorized, operation, "HH rejected profile update authorization", nil)
	case response.StatusCode == http.StatusTooManyRequests:
		failure := operationError(core.ErrorRateLimited, operation, "HH profile update was rate limited", nil)
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return failure
	case response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusUnprocessableEntity:
		return operationError(core.ErrorValidationRequired, operation, "HH rejected one or more resume fields", nil)
	case response.StatusCode >= 300 && response.StatusCode < 400 || response.StatusCode >= 500:
		return operationError(core.ErrorAmbiguousResult, operation, fmt.Sprintf("HH profile update outcome is unknown after status %d", response.StatusCode), nil)
	default:
		return operationError(core.ErrorPermanentFailure, operation, fmt.Sprintf("HH rejected profile update with status %d", response.StatusCode), nil)
	}
}

func profileStateConflict(err error) error {
	if errors.Is(err, core.ErrProfileStateChanged) {
		return operationError(core.ErrorConflict, "profile_state.apply.browser", "HH profile state changed after the proposal was created", err)
	}
	return err
}
