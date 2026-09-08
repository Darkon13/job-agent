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
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const maxResumeProfileResponse = 4 << 20

var resumeProfileSections = map[string]struct{}{
	"profile":               {},
	"resume":                {},
	"creds":                 {},
	"additional_properties": {},
}

var _ adapter.ProfileStateReader = (*ReadClient)(nil)
var _ adapter.ProfileStateWriter = (*ReadClient)(nil)

// ReadProfileState reads only fields declared under the native HH
// resume_profile sections:
// /resumes/<id>/{profile,resume,creds,additional_properties}/...
func (client *ReadClient) ReadProfileState(ctx context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, error) {
	if request.ProfileID == "" || request.ProfileID != client.profileID {
		return core.ProfileStateObservation{}, errors.New("HH API profile state reader profile does not match")
	}
	select {
	case <-ctx.Done():
		return core.ProfileStateObservation{}, ctx.Err()
	case <-client.profileLock:
	}
	defer func() { client.profileLock <- struct{}{} }()
	observation, _, err := client.readResumeProfileStateUnlocked(ctx, request)
	return observation, err
}

func (client *ReadClient) ApplyProfileState(ctx context.Context, proposal core.ProfileStateProposal) (adapter.ProfileStateApplyResult, error) {
	if err := proposal.Validate(); err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	if proposal.ProfileID != client.profileID {
		return adapter.ProfileStateApplyResult{}, errors.New("HH API profile state writer profile does not match")
	}
	paths, err := proposal.DeclaredPaths()
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	select {
	case <-ctx.Done():
		return adapter.ProfileStateApplyResult{}, ctx.Err()
	case <-client.profileLock:
	}
	defer func() { client.profileLock <- struct{}{} }()

	request := adapter.ProfileStateReadRequest{ProfileID: proposal.ProfileID, Paths: paths}
	observation, documents, err := client.readResumeProfileStateUnlocked(ctx, request)
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
	desired, err := decodeJSONObject(proposal.DesiredState)
	if err != nil {
		return adapter.ProfileStateApplyResult{}, fmt.Errorf("decode HH desired resume profile: %w", err)
	}
	desiredResumes, _ := desired["resumes"].(map[string]any)
	resumeIDs := pendingResumeProfileIDs(pending)
	for _, resumeID := range resumeIDs {
		desiredResume, ok := desiredResumes[resumeID].(map[string]any)
		if !ok {
			return adapter.ProfileStateApplyResult{}, errors.New("HH desired state has no resume profile document")
		}
		body, err := mergeResumeProfileUpdate(documents[resumeID], desiredResume)
		if err != nil {
			return adapter.ProfileStateApplyResult{}, err
		}
		if err := client.putResumeProfileUnlocked(ctx, resumeID, body); err != nil {
			if core.ErrorIsCategory(err, core.ErrorAmbiguousResult) {
				if reconciled, _, readErr := client.readResumeProfileStateUnlocked(ctx, request); readErr == nil {
					if remaining, changeErr := proposal.ChangesToApply(reconciled); changeErr == nil && len(remaining) == 0 {
						return adapter.ProfileStateApplyResult{Observation: reconciled}, nil
					}
				}
			}
			return adapter.ProfileStateApplyResult{}, err
		}
	}
	result, _, err := client.readResumeProfileStateUnlocked(ctx, request)
	if err != nil {
		return adapter.ProfileStateApplyResult{}, err
	}
	remaining, err := proposal.ChangesToApply(result)
	if err != nil {
		return adapter.ProfileStateApplyResult{}, profileStateConflict(err)
	}
	if len(remaining) != 0 {
		return adapter.ProfileStateApplyResult{}, operationError(core.ErrorAmbiguousResult, "profile_state.apply.api", "HH accepted the update but read-back did not confirm every field", nil)
	}
	return adapter.ProfileStateApplyResult{Observation: result}, nil
}

func (client *ReadClient) readResumeProfileStateUnlocked(ctx context.Context, request adapter.ProfileStateReadRequest) (core.ProfileStateObservation, map[string]map[string]any, error) {
	const operation = "profile_state.read.api"
	if len(request.Paths) == 0 {
		return core.ProfileStateObservation{}, nil, errors.New("HH API profile state reader requires declared paths")
	}
	pathsByResume := make(map[string][]resumeProfilePath)
	for _, pointer := range request.Paths {
		parsed, ok := parseResumeProfilePath(pointer)
		if !ok {
			return core.ProfileStateObservation{}, nil, operationError(core.ErrorUnsupported, operation, "HH API reader supports only native resume_profile section paths", nil)
		}
		pathsByResume[parsed.resumeID] = append(pathsByResume[parsed.resumeID], parsed)
	}
	resumeIDs := make([]string, 0, len(pathsByResume))
	for resumeID := range pathsByResume {
		resumeIDs = append(resumeIDs, resumeID)
	}
	sort.Strings(resumeIDs)
	observedResumes := make(map[string]any, len(resumeIDs))
	documents := make(map[string]map[string]any, len(resumeIDs))
	for _, resumeID := range resumeIDs {
		document, err := client.getResumeProfileUnlocked(ctx, resumeID)
		if err != nil {
			return core.ProfileStateObservation{}, nil, err
		}
		documents[resumeID] = document
		observedResume := make(map[string]any)
		for _, path := range pathsByResume[resumeID] {
			value, exists := lookupObjectPath(document, append([]string{path.section}, path.fields...))
			if exists {
				setObjectPath(observedResume, append([]string{path.section}, path.fields...), value)
			}
		}
		observedResumes[resumeID] = observedResume
	}
	state, err := json.Marshal(map[string]any{"resumes": observedResumes})
	if err != nil {
		return core.ProfileStateObservation{}, nil, fmt.Errorf("encode HH resume profile observation: %w", err)
	}
	observation, err := core.NewProfileStateObservation(request.ProfileID, state, "", time.Now().UTC())
	return observation, documents, err
}

func (client *ReadClient) getResumeProfileUnlocked(ctx context.Context, resumeID string) (map[string]any, error) {
	endpoint := strings.TrimRight(client.apiBaseURL, "/") + "/resume_profile/" + url.PathEscape(resumeID)
	var document map[string]any
	if err := client.resumeProfileJSONRequest(ctx, http.MethodGet, endpoint, nil, &document); err != nil {
		return nil, err
	}
	for section := range resumeProfileSections {
		if value, exists := document[section]; exists {
			if _, ok := value.(map[string]any); !ok {
				return nil, operationError(core.ErrorPermanentFailure, "profile_state.read.api", "HH returned an invalid resume_profile section", nil)
			}
		}
	}
	return document, nil
}

func (client *ReadClient) putResumeProfileUnlocked(ctx context.Context, resumeID string, body map[string]any) error {
	endpoint := strings.TrimRight(client.apiBaseURL, "/") + "/resume_profile/" + url.PathEscape(resumeID)
	return client.resumeProfileJSONRequest(ctx, http.MethodPut, endpoint, body, nil)
}

func (client *ReadClient) resumeProfileJSONRequest(ctx context.Context, method, endpoint string, body any, target any) error {
	credentials, err := loadOAuthCredentials(client.credentialsRef)
	if err != nil {
		return err
	}
	var encoded io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode HH resume_profile request: %w", err)
		}
		encoded = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, encoded)
	if err != nil {
		return fmt.Errorf("create HH resume_profile request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	request.Header.Set("HH-User-Agent", client.userAgent)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	httpClient := *client.httpClient
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		category := core.ErrorTemporaryFailure
		if method != http.MethodGet {
			category = core.ErrorAmbiguousResult
		}
		return operationError(category, resumeProfileOperation(method), "HH resume_profile request failed", err)
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxResumeProfileResponse))
	if readErr != nil {
		category := core.ErrorTemporaryFailure
		if method != http.MethodGet {
			category = core.ErrorAmbiguousResult
		}
		return operationError(category, resumeProfileOperation(method), "HH resume_profile response could not be read", readErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return classifyResumeProfileResponse(method, response)
	}
	if target != nil {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(target); err != nil {
			return operationError(core.ErrorTemporaryFailure, resumeProfileOperation(method), "HH returned an invalid resume_profile response", err)
		}
	}
	return nil
}

func classifyResumeProfileResponse(method string, response *http.Response) error {
	operation := resumeProfileOperation(method)
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return operationError(core.ErrorUnauthorized, operation, "HH rejected resume_profile authorization", nil)
	case response.StatusCode == http.StatusTooManyRequests:
		failure := operationError(core.ErrorRateLimited, operation, "HH resume_profile request was rate limited", nil)
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
		return failure
	case response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusUnprocessableEntity:
		return operationError(core.ErrorValidationRequired, operation, "HH rejected one or more resume_profile fields", nil)
	case method != http.MethodGet && (response.StatusCode >= 300 && response.StatusCode < 400 || response.StatusCode >= 500):
		return operationError(core.ErrorAmbiguousResult, operation, fmt.Sprintf("HH resume_profile update outcome is unknown after status %d", response.StatusCode), nil)
	case response.StatusCode >= 500:
		return operationError(core.ErrorTemporaryFailure, operation, fmt.Sprintf("HH returned status %d", response.StatusCode), nil)
	default:
		return operationError(core.ErrorPermanentFailure, operation, fmt.Sprintf("HH rejected resume_profile request with status %d", response.StatusCode), nil)
	}
}

func resumeProfileOperation(method string) string {
	if method == http.MethodGet {
		return "profile_state.read.api"
	}
	return "profile_state.apply.api"
}

type resumeProfilePath struct {
	resumeID string
	section  string
	fields   []string
}

func parseResumeProfilePath(pointer string) (resumeProfilePath, bool) {
	if !strings.HasPrefix(pointer, "/") {
		return resumeProfilePath{}, false
	}
	raw := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	if len(raw) < 4 || raw[0] != "resumes" {
		return resumeProfilePath{}, false
	}
	segments := make([]string, len(raw))
	for index, value := range raw {
		decoded, ok := unescapeResumeProfilePointer(value)
		if !ok || strings.TrimSpace(decoded) == "" {
			return resumeProfilePath{}, false
		}
		segments[index] = decoded
	}
	if _, allowed := resumeProfileSections[segments[2]]; !allowed {
		return resumeProfilePath{}, false
	}
	return resumeProfilePath{resumeID: segments[1], section: segments[2], fields: segments[3:]}, true
}

func unescapeResumeProfilePointer(value string) (string, bool) {
	var result strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '~' {
			result.WriteByte(value[index])
			continue
		}
		if index+1 >= len(value) {
			return "", false
		}
		index++
		switch value[index] {
		case '0':
			result.WriteByte('~')
		case '1':
			result.WriteByte('/')
		default:
			return "", false
		}
	}
	return result.String(), true
}

func lookupObjectPath(root map[string]any, path []string) (any, bool) {
	var current any = root
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func setObjectPath(root map[string]any, path []string, value any) {
	current := root
	for index, segment := range path {
		if index == len(path)-1 {
			current[segment] = value
			return
		}
		next, ok := current[segment].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[segment] = next
		}
		current = next
	}
}

func decodeJSONObject(raw json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func pendingResumeProfileIDs(changes []core.ProfileStateChange) []string {
	seen := make(map[string]struct{})
	for _, change := range changes {
		path, ok := parseResumeProfilePath(change.Path)
		if ok {
			seen[path.resumeID] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for resumeID := range seen {
		result = append(result, resumeID)
	}
	sort.Strings(result)
	return result
}

func mergeResumeProfileUpdate(current, desired map[string]any) (map[string]any, error) {
	if current == nil {
		return nil, errors.New("HH resume_profile update requires a fresh platform document")
	}
	body := make(map[string]any)
	if screen, exists := current["current_screen_id"]; exists {
		body["current_screen_id"] = screen
	}
	for section := range resumeProfileSections {
		currentSection, currentExists := current[section]
		overlay, declared := desired[section]
		if !currentExists && !declared {
			continue
		}
		base := make(map[string]any)
		if currentExists {
			var ok bool
			base, ok = currentSection.(map[string]any)
			if !ok {
				return nil, operationError(core.ErrorPermanentFailure, "profile_state.apply.api", "HH resume_profile section must be an object", nil)
			}
		}
		copy := cloneJSONObject(base)
		if declared {
			overlayObject, ok := overlay.(map[string]any)
			if !ok {
				return nil, operationError(core.ErrorValidationRequired, "profile_state.apply.api", "HH resume_profile section must be an object", nil)
			}
			mergeJSONObject(copy, overlayObject)
		}
		body[section] = copy
	}
	if _, ok := body["resume"].(map[string]any); !ok {
		return nil, errors.New("HH resume_profile update requires resume section")
	}
	return body, nil
}

func mergeJSONObject(target, overlay map[string]any) {
	for key, value := range overlay {
		if overlayObject, ok := value.(map[string]any); ok {
			if targetObject, exists := target[key].(map[string]any); exists {
				mergeJSONObject(targetObject, overlayObject)
				continue
			}
			target[key] = cloneJSONObject(overlayObject)
			continue
		}
		// Arrays are atomic in desired state: assigning one replaces the whole
		// remote array, while undeclared arrays remain untouched in target.
		target[key] = value
	}
}

func cloneJSONObject(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		if object, ok := value.(map[string]any); ok {
			result[key] = cloneJSONObject(object)
			continue
		}
		result[key] = value
	}
	return result
}
