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
	"sort"
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
	values, err := desiredHHBrowserResumeValues(proposal.DesiredState, pending)
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	resumeIDs := make([]string, 0, len(values))
	for resumeID := range values {
		resumeIDs = append(resumeIDs, resumeID)
	}
	sort.Strings(resumeIDs)
	for _, resumeID := range resumeIDs {
		writes := []struct {
			fields map[string]any
			apply  func(context.Context, string, map[string]any) error
		}{
			{fields: values[resumeID].profile, apply: client.postProfileFields},
			{fields: values[resumeID].resume, apply: client.postResumeFields},
		}
		for _, write := range writes {
			if len(write.fields) == 0 {
				continue
			}
			if err := write.apply(ctx, resumeID, write.fields); err != nil {
				if core.ErrorIsCategory(err, core.ErrorAmbiguousResult) {
					if reconciled, reconcileErr := client.readBack(ctx, proposal, paths); reconcileErr == nil && len(reconciled.pending) == 0 {
						return adapter.ProfileStateApplyResult{Observation: reconciled.observation}, nil
					}
				}
				return adapter.ProfileStateApplyResult{}, err
			}
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

func (client *BrowserProfileStateClient) postResumeFields(ctx context.Context, resumeID string, fields map[string]any) error {
	const operation = "profile_state.apply.browser"
	endpoint, _ := url.Parse(strings.TrimRight(client.webBaseURL, "/") + "/applicant/resume/edit")
	query := endpoint.Query()
	query.Set("resume", resumeID)
	query.Set("hhtmSource", "profile-state")
	endpoint.RawQuery = query.Encode()
	body, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encode HH resume update: %w", err)
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

func (client *BrowserProfileStateClient) postProfileFields(ctx context.Context, resumeID string, fields map[string]any) error {
	const operation = "profile_state.apply.browser"
	endpoint, _ := url.Parse(strings.TrimRight(client.webBaseURL, "/") + "/shards/applicant/profile/update")
	body, err := json.Marshal(map[string]any{"profile": fields})
	if err != nil {
		return fmt.Errorf("encode HH profile update: %w", err)
	}
	httpClient, err := client.authenticatedClient(endpoint.String())
	if err != nil {
		return err
	}
	copy := *httpClient
	copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create HH profile update request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", client.reader.userAgent)
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	request.Header.Set("Referer", strings.TrimRight(client.webBaseURL, "/")+"/resume/edit/"+url.PathEscape(resumeID))
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

type desiredHHBrowserResumeState struct {
	resume  map[string]any
	profile map[string]any
}

func desiredHHBrowserResumeValues(state json.RawMessage, pending []core.ProfileStateChange) (map[string]*desiredHHBrowserResumeState, error) {
	desired, err := decodeJSONObject(state)
	if err != nil {
		return nil, fmt.Errorf("decode HH desired profile state: %w", err)
	}
	desiredResumes, ok := desired["resumes"].(map[string]any)
	if !ok {
		return nil, errors.New("HH desired profile state has no resumes object")
	}
	values := make(map[string]*desiredHHBrowserResumeState, len(pending))
	for _, change := range pending {
		if path, supported := parseBrowserResumePath(change.Path); supported {
			desiredResume, ok := desiredResumes[path.resumeID].(map[string]any)
			if !ok {
				return nil, errors.New("HH desired profile state has no browser resume document")
			}
			web, ok := desiredResume["web"].(map[string]any)
			if !ok {
				return nil, errors.New("HH desired profile state has no web resume fields")
			}
			value, exists := web[path.field]
			if !exists {
				return nil, errors.New("HH desired profile state has no declared browser resume field")
			}
			if _, wrapped := browserResumeWrappedFields[path.field]; wrapped && value != nil {
				if _, ok := value.([]any); !ok {
					return nil, operationError(core.ErrorValidationRequired, "profile_state.apply.browser", "HH wrapped browser resume fields must be arrays", nil)
				}
			}
			entry := browserDesiredEntry(values, path.resumeID)
			entry.resume[path.field] = value
			continue
		}
		if path, supported := parseBrowserProfilePath(change.Path); supported {
			desiredResume, ok := desiredResumes[path.resumeID].(map[string]any)
			if !ok {
				return nil, errors.New("HH desired profile state has no browser profile document")
			}
			profile, ok := desiredResume["web_profile"].(map[string]any)
			if !ok {
				return nil, errors.New("HH desired profile state has no web_profile fields")
			}
			value, exists := profile[path.field]
			if !exists {
				return nil, errors.New("HH desired profile state has no declared browser profile field")
			}
			if _, ok := value.([]any); !ok {
				return nil, operationError(core.ErrorValidationRequired, "profile_state.apply.browser", "HH browser profile fields must be arrays", nil)
			}
			browserDesiredEntry(values, path.resumeID).profile[path.field] = value
			continue
		}
		resumeID, supported := hhAboutResumeID(change.Path)
		if !supported {
			return nil, operationError(core.ErrorUnsupported, "profile_state.apply.browser", "HH browser writer does not support one or more declared fields", nil)
		}
		desiredResume, ok := desiredResumes[resumeID].(map[string]any)
		if !ok {
			return nil, errors.New("HH desired profile state has no resume document")
		}
		value, exists := desiredResume["about"]
		if !exists {
			return nil, errors.New("HH desired profile state has no declared about value")
		}
		entry := browserDesiredEntry(values, resumeID)
		if _, collision := entry.resume["skills"]; collision {
			return nil, operationError(core.ErrorValidationRequired, "profile_state.apply.browser", "do not declare both legacy about and web.skills", nil)
		}
		if value == nil {
			entry.resume["skills"] = []any{}
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, operationError(core.ErrorValidationRequired, "profile_state.apply.browser", "HH resume about must be a string or null", nil)
		}
		if text == "" {
			return nil, operationError(core.ErrorValidationRequired, "profile_state.apply.browser", "use null to clear HH resume about", nil)
		}
		entry.resume["skills"] = []any{text}
	}
	return values, nil
}

func browserDesiredEntry(values map[string]*desiredHHBrowserResumeState, resumeID string) *desiredHHBrowserResumeState {
	entry := values[resumeID]
	if entry == nil {
		entry = &desiredHHBrowserResumeState{resume: make(map[string]any), profile: make(map[string]any)}
		values[resumeID] = entry
	}
	return entry
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
