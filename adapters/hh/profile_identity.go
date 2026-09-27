package hh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

// Profile-state pointers of the applicant profile document. They belong to the
// HH web contract and are shared by the contacts resolver and the identity
// capture.
const (
	ProfileFirstNamePath      = "/web_profile/firstName"
	ProfileLastNamePath       = "/web_profile/lastName"
	ProfileEmailPath          = "/web/email"
	ProfileCommunicationsPath = "/web_profile/communicationMethods"
)

// ProfileContactPaths returns the profile-state pointers read for the sender
// name and contacts of one resume.
func ProfileContactPaths(resumeID string) []string {
	prefix := "/resumes/" + resumeID
	return []string{
		prefix + ProfileEmailPath,
		prefix + ProfileFirstNamePath,
		prefix + ProfileLastNamePath,
		prefix + ProfileCommunicationsPath,
	}
}

// ProfileContacts is the sender name and contacts extracted from a profile
// document. Values stay raw: masking and personal-data limits belong to the
// callers.
type ProfileContacts struct {
	FirstName string
	LastName  string
	Email     string
	Telegram  string
	Phone     string
}

// ContactsFromProfileState extracts the name and contacts of one resume from a
// profile-state observation.
func ContactsFromProfileState(observation core.ProfileStateObservation, resumeID string) ProfileContacts {
	prefix := "/resumes/" + resumeID
	read := func(path string, extract func(any) string) string {
		raw, exists, err := observation.ValueAt(path)
		if err != nil || !exists || string(raw) == "null" {
			return ""
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return ""
		}
		return extract(value)
	}
	return ProfileContacts{
		FirstName: read(prefix+ProfileFirstNamePath, firstObservedContactValue("string", "name", "value")),
		LastName:  read(prefix+ProfileLastNamePath, firstObservedContactValue("string", "name", "value")),
		Email:     read(prefix+ProfileEmailPath, firstObservedContactValue("string", "value", "email")),
		Telegram:  read(prefix+ProfileCommunicationsPath, firstObservedContactValue("telegram")),
		Phone:     read(prefix+ProfileCommunicationsPath, firstObservedPhoneValue),
	}
}

func firstObservedContactValue(keys ...string) func(any) string {
	return func(value any) string {
		switch item := value.(type) {
		case string:
			return strings.TrimSpace(item)
		case []any:
			for _, child := range item {
				if found := firstObservedContactValue(keys...)(child); found != "" {
					return found
				}
			}
		case map[string]any:
			for _, key := range keys {
				raw, exists := item[key]
				if !exists {
					continue
				}
				if text, ok := raw.(string); ok {
					if text = strings.TrimSpace(text); text != "" {
						return text
					}
				}
			}
		}
		return ""
	}
}

// firstObservedPhoneValue finds the value of a communication method that looks
// like a phone number.
func firstObservedPhoneValue(value any) string {
	switch item := value.(type) {
	case []any:
		for _, child := range item {
			if found := firstObservedPhoneValue(child); found != "" {
				return found
			}
		}
	case map[string]any:
		kind := ""
		for _, key := range []string{"type", "name", "kind", "subtype", "id"} {
			if raw, exists := item[key]; exists {
				if text, ok := raw.(string); ok {
					kind += " " + strings.ToLower(text)
				}
			}
		}
		looksLikePhone := strings.Contains(kind, "phone") || strings.Contains(kind, "тел")
		value := ""
		for _, key := range []string{"value", "phone", "text"} {
			if raw, exists := item[key]; exists {
				if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
					value = strings.TrimSpace(text)
					break
				}
			}
		}
		if looksLikePhone && value != "" {
			return value
		}
		for _, child := range item {
			if found := firstObservedPhoneValue(child); found != "" && looksLikePhone {
				return found
			}
		}
	}
	return ""
}

// ReadProfileIdentity reads the applicant profile page for the resume list and,
// when at least one resume exists, the name and contacts of the first resume.
// Only GET requests are used, so the call is safe right after a login wrote the
// state file. Missing name or contacts do not fail the capture.
func (client *BrowserReadClient) ReadProfileIdentity(ctx context.Context, profileID core.ProfileID) (adapter.ProfileIdentitySnapshot, error) {
	if profileID == "" || profileID != client.profileID {
		return adapter.ProfileIdentitySnapshot{}, errors.New("HH profile identity reader profile does not match")
	}
	snapshot := adapter.ProfileIdentitySnapshot{CapturedAt: time.Now().UTC()}
	resumes, accountID, err := client.readResumeCatalog(ctx)
	if err != nil {
		return adapter.ProfileIdentitySnapshot{}, err
	}
	snapshot.Resumes = resumes
	if accountID != "" {
		digest := sha256.Sum256([]byte("hh\x00account\x00" + accountID))
		snapshot.AccountHash = "sha256:" + hex.EncodeToString(digest[:6])
	}
	if len(resumes) == 0 {
		return snapshot, nil
	}
	observation, err := client.ReadProfileState(ctx, adapter.ProfileStateReadRequest{
		ProfileID: profileID, Paths: ProfileContactPaths(resumes[0].ID),
	})
	if err != nil {
		return snapshot, nil
	}
	contacts := ContactsFromProfileState(observation, resumes[0].ID)
	snapshot.DisplayName = strings.TrimSpace(strings.TrimSpace(contacts.FirstName) + " " + strings.TrimSpace(contacts.LastName))
	snapshot.Email = maskEmail(contacts.Email)
	snapshot.Phone = maskPhone(contacts.Phone)
	return snapshot, nil
}

// readResumeCatalog reads the applicant profile page and returns every resume
// with its title plus the platform account id when the page exposes one.
func (client *BrowserReadClient) readResumeCatalog(ctx context.Context) ([]adapter.ProfileIdentityResume, string, error) {
	const operation = "profile.identity.browser"
	transport, err := NewResumeTouchTransport(client.stateFile, client.httpClient)
	if err != nil {
		return nil, "", err
	}
	httpClient, err := transport.authenticatedClient()
	if err != nil {
		return nil, "", err
	}
	endpoint := strings.TrimRight(client.webBaseURL, "/") + "/applicant/profile/me"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create HH profile identity request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	request.Header.Set("User-Agent", client.userAgent)
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, "", &core.OperationError{Category: core.ErrorTemporaryFailure, Operation: operation, Platform: Name, Message: "profile identity request failed", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, "", &core.OperationError{Category: core.ErrorUnauthorized, Operation: operation, Platform: Name, Message: "browser session is unauthorized"}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, "", err
	}
	match := initialStatePattern.FindSubmatch(data)
	if len(match) != 2 {
		return nil, "", &core.OperationError{
			Category: core.ErrorTemporaryFailure, Operation: operation, Platform: Name,
			Message:  "authenticated profile state was not found",
			Metadata: map[string]string{"status": fmt.Sprint(response.StatusCode), "final_path": response.Request.URL.Path},
		}
	}
	decoded := []byte(html.UnescapeString(string(match[1])))
	var state struct {
		ApplicantResumes []struct {
			Attributes struct {
				ID    json.RawMessage `json:"id"`
				Title string          `json:"title"`
				Name  string          `json:"name"`
				Owner json.RawMessage `json:"ownerId"`
			} `json:"_attributes"`
		} `json:"applicantResumes"`
		ApplicantID json.RawMessage `json:"applicantId"`
		UserID      json.RawMessage `json:"userId"`
		AccountID   json.RawMessage `json:"accountId"`
	}
	if err := json.Unmarshal(decoded, &state); err != nil {
		return nil, "", fmt.Errorf("decode HH profile identity state: %w", err)
	}
	resumes := make([]adapter.ProfileIdentityResume, 0, len(state.ApplicantResumes))
	for _, resume := range state.ApplicantResumes {
		id := rawIdentityValue(resume.Attributes.ID)
		if id == "" {
			continue
		}
		title := strings.TrimSpace(resume.Attributes.Title)
		if title == "" {
			title = strings.TrimSpace(resume.Attributes.Name)
		}
		resumes = append(resumes, adapter.ProfileIdentityResume{ID: id, Title: title})
	}
	accountID := rawIdentityValue(state.ApplicantID)
	if accountID == "" {
		accountID = rawIdentityValue(state.UserID)
	}
	if accountID == "" {
		accountID = rawIdentityValue(state.AccountID)
	}
	if accountID == "" && len(state.ApplicantResumes) != 0 {
		accountID = rawIdentityValue(state.ApplicantResumes[0].Attributes.Owner)
	}
	return resumes, accountID, nil
}

// rawIdentityValue reads a JSON scalar that may be a string or a number.
func rawIdentityValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return strings.TrimSpace(number.String())
	}
	return ""
}

// maskEmail keeps the domain and the first character, for example
// `a***@example.test`.
func maskEmail(value string) string {
	value = strings.TrimSpace(value)
	at := strings.LastIndex(value, "@")
	if at <= 0 || at == len(value)-1 {
		return ""
	}
	local, domain := value[:at], value[at+1:]
	if len(local) == 0 {
		return ""
	}
	return local[:1] + "***@" + domain
}

// maskPhone keeps the first two and the last two characters of a phone number.
func maskPhone(value string) string {
	digits := make([]rune, 0, len(value))
	for _, symbol := range strings.TrimSpace(value) {
		if symbol >= '0' && symbol <= '9' {
			digits = append(digits, symbol)
		}
	}
	if len(digits) < 4 {
		return ""
	}
	masked := make([]rune, 0, len(digits))
	masked = append(masked, digits[0], digits[1])
	for index := 2; index < len(digits)-2; index++ {
		masked = append(masked, '*')
	}
	masked = append(masked, digits[len(digits)-2], digits[len(digits)-1])
	return string(masked)
}

var _ adapter.ProfileIdentityReader = (*BrowserReadClient)(nil)
