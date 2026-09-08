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

	"github.com/Darkon13/job-agent/core"
)

// The browser resume editor exposes its editable state as wrapper arrays for
// most scalar/list fields and as native JSON for compound fields. These names
// come from the current /applicant/resume editor contract. Keeping an allowlist
// prevents a bootstrap document from posting read-only status/telemetry fields.
var browserResumeWrappedFields = map[string]struct{}{
	"lang": {}, "title": {}, "email": {}, "photo": {}, "skills": {},
	"keySkills": {}, "portfolio": {}, "accessType": {}, "travelTime": {},
	"workFormats": {}, "hiddenFields": {}, "setkaAccess": {}, "autoHideTime": {},
	"educationLevel": {}, "employmentForms": {}, "professionalRole": {},
	"preferredContact": {}, "businessTripReadiness": {},
}

var browserProfileFields = map[string]struct{}{
	"additionalEducation": {}, "addressCoordinates": {}, "area": {},
	"attestationEducation": {}, "birthday": {}, "citizenship": {},
	"communicationMethods": {}, "driverLicenseTypes": {}, "educationLevel": {},
	"elementaryEducation": {}, "experience": {}, "firstName": {}, "gender": {},
	"hasMedicalBook": {}, "hasSelfEmployment": {}, "hasVehicle": {},
	"language": {}, "lastName": {}, "metro": {}, "middleName": {},
	"otherCommunicationMethods": {}, "preferredWorkAreas": {},
	"primaryEducation": {}, "relocation": {}, "relocationArea": {},
	"relocationDistrict": {}, "setkaRespectsVisibility": {}, "workTicket": {},
}

var browserResumeRawFields = map[string]struct{}{
	"phone": {}, "salary": {}, "proftest": {}, "experience": {}, "certificate": {},
	"personalSite": {}, "recommendation": {}, "primaryEducation": {},
	"additionalEducation": {}, "elementaryEducation": {}, "attestationEducation": {},
}

type browserResumePath struct {
	resumeID string
	field    string
	fields   []string
}

type browserProfilePath struct {
	resumeID string
	field    string
	fields   []string
}

func parseBrowserResumePath(pointer string) (browserResumePath, bool) {
	if !strings.HasPrefix(pointer, "/") {
		return browserResumePath{}, false
	}
	raw := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	if len(raw) < 4 || raw[0] != "resumes" || raw[2] != "web" {
		return browserResumePath{}, false
	}
	segments := make([]string, len(raw))
	for index, value := range raw {
		decoded, ok := unescapeResumeProfilePointer(value)
		if !ok || strings.TrimSpace(decoded) == "" {
			return browserResumePath{}, false
		}
		segments[index] = decoded
	}
	if _, wrapped := browserResumeWrappedFields[segments[3]]; !wrapped {
		if _, raw := browserResumeRawFields[segments[3]]; !raw {
			return browserResumePath{}, false
		}
	}
	return browserResumePath{resumeID: segments[1], field: segments[3], fields: segments[4:]}, true
}

func parseBrowserProfilePath(pointer string) (browserProfilePath, bool) {
	if !strings.HasPrefix(pointer, "/") {
		return browserProfilePath{}, false
	}
	raw := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	if len(raw) < 4 || raw[0] != "resumes" || raw[2] != "web_profile" {
		return browserProfilePath{}, false
	}
	segments := make([]string, len(raw))
	for index, value := range raw {
		decoded, ok := unescapeResumeProfilePointer(value)
		if !ok || strings.TrimSpace(decoded) == "" {
			return browserProfilePath{}, false
		}
		segments[index] = decoded
	}
	if _, allowed := browserProfileFields[segments[3]]; !allowed {
		return browserProfilePath{}, false
	}
	return browserProfilePath{resumeID: segments[1], field: segments[3], fields: segments[4:]}, true
}

func (client *BrowserReadClient) readBrowserResumeDocuments(ctx context.Context, resumeIDs []string) (map[string]map[string]any, error) {
	documents := make(map[string]map[string]any, len(resumeIDs))
	for _, resumeID := range resumeIDs {
		document, err := client.getBrowserResumeDocument(ctx, resumeID)
		if err != nil {
			return nil, err
		}
		documents[resumeID] = document
	}
	return documents, nil
}

func (client *BrowserReadClient) getBrowserResumeDocument(ctx context.Context, resumeID string) (map[string]any, error) {
	const operation = "profile_state.read.browser"
	endpoint, _ := url.Parse(strings.TrimRight(client.webBaseURL, "/") + "/applicant/resume")
	query := endpoint.Query()
	query.Set("resume", resumeID)
	endpoint.RawQuery = query.Encode()

	client.mu.Lock()
	defer client.mu.Unlock()
	transport, err := NewResumeTouchTransport(client.stateFile, client.httpClient)
	if err != nil {
		return nil, err
	}
	transport.profileURL = endpoint.String()
	transport.touchURL = endpoint.String()
	httpClient, err := transport.authenticatedClient()
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create HH browser resume request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	request.Header.Set("User-Agent", client.userAgent)
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, operationError(core.ErrorTemporaryFailure, operation, "HH browser resume read failed", err)
	}
	defer response.Body.Close()
	if err := classifyBrowserReadResponse(response, operation); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBrowserProfileStateResponse))
	if err != nil {
		return nil, operationError(core.ErrorTemporaryFailure, operation, "HH browser resume response could not be read", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var payload struct {
		Resume map[string]any `json:"resume"`
	}
	if err := decoder.Decode(&payload); err != nil {
		return nil, operationError(core.ErrorTemporaryFailure, operation, "HH returned an invalid browser resume document", err)
	}
	if payload.Resume == nil {
		return nil, operationError(core.ErrorPermanentFailure, operation, "HH browser resume response has no resume document", nil)
	}
	return payload.Resume, nil
}

func (client *BrowserReadClient) getBrowserProfileDocument(ctx context.Context, resumeID string) (map[string]any, error) {
	const operation = "profile_state.read.browser"
	endpoint, _ := url.Parse(strings.TrimRight(client.webBaseURL, "/") + "/shards/applicant/profile/get_full_data")
	query := endpoint.Query()
	query.Set("resumeHash", resumeID)
	endpoint.RawQuery = query.Encode()

	client.mu.Lock()
	defer client.mu.Unlock()
	transport, err := NewResumeTouchTransport(client.stateFile, client.httpClient)
	if err != nil {
		return nil, err
	}
	transport.profileURL = endpoint.String()
	transport.touchURL = endpoint.String()
	httpClient, err := transport.authenticatedClient()
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create HH browser profile request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	request.Header.Set("User-Agent", client.userAgent)
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	response, err := httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, operationError(core.ErrorTemporaryFailure, operation, "HH browser profile read failed", err)
	}
	defer response.Body.Close()
	if err := classifyBrowserReadResponse(response, operation); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBrowserProfileStateResponse))
	decoder.UseNumber()
	var payload struct {
		Profile struct {
			Fields map[string]any `json:"fields"`
		} `json:"profile"`
	}
	if err := decoder.Decode(&payload); err != nil {
		return nil, operationError(core.ErrorTemporaryFailure, operation, "HH returned an invalid browser profile document", err)
	}
	if payload.Profile.Fields == nil {
		return nil, operationError(core.ErrorPermanentFailure, operation, "HH browser profile response has no fields", nil)
	}
	return payload.Profile.Fields, nil
}

func normalizeBrowserResumeField(field string, value any) (any, error) {
	if _, raw := browserResumeRawFields[field]; raw {
		return value, nil
	}
	if _, wrapped := browserResumeWrappedFields[field]; !wrapped {
		return nil, errors.New("unsupported HH browser resume field")
	}
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, operationError(core.ErrorPermanentFailure, "profile_state.read.browser", "HH returned an invalid wrapped resume field", nil)
	}
	result := make([]any, 0, len(items))
	for _, item := range items {
		wrapper, ok := item.(map[string]any)
		if !ok {
			return nil, operationError(core.ErrorPermanentFailure, "profile_state.read.browser", "HH returned an invalid wrapped resume item", nil)
		}
		value, exists := wrapper["string"]
		if !exists {
			return nil, operationError(core.ErrorPermanentFailure, "profile_state.read.browser", "HH returned a wrapped resume item without string", nil)
		}
		result = append(result, value)
	}
	return result, nil
}

func browserResumeReadField(field string) string {
	if field == "lang" {
		return "language"
	}
	return field
}

func normalizeBrowserProfileField(value any) any {
	items, ok := value.([]any)
	if !ok {
		return value
	}
	result := make([]any, 0, len(items))
	for _, item := range items {
		wrapper, ok := item.(map[string]any)
		if !ok {
			return value
		}
		unwrapped, exists := wrapper["string"]
		if !exists {
			return value
		}
		result = append(result, unwrapped)
	}
	return result
}

func browserResumeIDs(paths map[string][]browserResumePath) []string {
	result := make([]string, 0, len(paths))
	for resumeID := range paths {
		result = append(result, resumeID)
	}
	sort.Strings(result)
	return result
}

func browserProfileResumeIDs(paths map[string][]browserProfilePath) []string {
	result := make([]string, 0, len(paths))
	for resumeID := range paths {
		result = append(result, resumeID)
	}
	sort.Strings(result)
	return result
}
