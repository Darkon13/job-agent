package hh

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const browserNegotiationsMaxPages = 200

var _ adapter.ApplicationStateObserver = (*BrowserReadClient)(nil)

// hhNegotiationInitialState is the subset of the negotiations page state the
// observer reads. The web page renders the same topic list as the OAuth
// negotiations API, but under the Lux initial state template.
type hhNegotiationInitialState struct {
	ApplicantNegotiations struct {
		TopicList []struct {
			ID               flexibleID `json:"id"`
			VacancyID        flexibleID `json:"vacancyId"`
			LastState        string     `json:"lastState"`
			ViewedByOpponent *bool      `json:"viewedByOpponent"`
			LastModified     string     `json:"lastModified"`
		} `json:"topicList"`
		PageCount flexibleID `json:"pageCount"`
	} `json:"applicantNegotiations"`
}

// ObserveApplicationStates reads every negotiation topic through the logged-in
// browser session. It is read-only: the page is fetched with GET and nothing is
// submitted.
func (client *BrowserReadClient) ObserveApplicationStates(ctx context.Context, profileID core.ProfileID) (adapter.ApplicationStateObservationResult, error) {
	if profileID == "" || profileID != client.profileID {
		return adapter.ApplicationStateObservationResult{}, errors.New("HH browser application observation profile does not match")
	}
	observedAt := time.Now().UTC()
	applications := make([]adapter.ApplicationStateObservation, 0)
	seen := make(map[string]struct{})
	pageCount := -1
	for page := 0; page < browserNegotiationsMaxPages; page++ {
		if pageCount >= 0 && page >= pageCount {
			return adapter.ApplicationStateObservationResult{Applications: applications, ObservedAt: observedAt}, nil
		}
		endpoint := strings.TrimRight(client.webBaseURL, "/") + "/applicant/negotiations?" + url.Values{
			"status": {"all"},
			"page":   {strconv.Itoa(page)},
		}.Encode()
		document, finalURL, err := client.getHTML(ctx, endpoint, "applications.observe.browser")
		if err != nil {
			return adapter.ApplicationStateObservationResult{}, err
		}
		if isLoginURL(finalURL) {
			return adapter.ApplicationStateObservationResult{}, operationError(
				core.ErrorUnauthorized, "applications.observe.browser",
				"HH browser session requires authentication", nil,
			)
		}
		rendered, err := renderHTMLNode(document)
		if err != nil {
			return adapter.ApplicationStateObservationResult{}, operationError(
				core.ErrorTemporaryFailure, "applications.observe.browser",
				"HH negotiations page could not be rendered", err,
			)
		}
		raw, err := extractInitialStateByMarkers(rendered, "HH-Lux-InitialState")
		if err != nil {
			return adapter.ApplicationStateObservationResult{}, operationError(
				core.ErrorTemporaryFailure, "applications.observe.browser",
				"HH negotiations page does not contain the expected initial state", err,
			)
		}
		var state hhNegotiationInitialState
		if err := json.Unmarshal([]byte(html.UnescapeString(string(raw))), &state); err != nil {
			return adapter.ApplicationStateObservationResult{}, operationError(
				core.ErrorTemporaryFailure, "applications.observe.browser",
				"HH negotiations page returned invalid state", err,
			)
		}
		topics := state.ApplicantNegotiations.TopicList
		if page == 0 {
			pageCount, err = strconv.Atoi(string(state.ApplicantNegotiations.PageCount))
			if err != nil || pageCount < 0 || (pageCount == 0 && len(topics) != 0) ||
				(pageCount > 0 && len(topics) == 0 && page+1 < pageCount) {
				return adapter.ApplicationStateObservationResult{}, operationError(
					core.ErrorTemporaryFailure, "applications.observe.browser",
					"HH negotiations pagination is invalid", nil,
				)
			}
		}
		if len(topics) == 0 {
			return adapter.ApplicationStateObservationResult{Applications: applications, ObservedAt: observedAt}, nil
		}
		for _, topic := range topics {
			negotiationID := strings.TrimSpace(string(topic.ID))
			vacancyID := strings.TrimSpace(string(topic.VacancyID))
			platformState := strings.TrimSpace(topic.LastState)
			if negotiationID == "" || vacancyID == "" || platformState == "" {
				return adapter.ApplicationStateObservationResult{}, operationError(
					core.ErrorTemporaryFailure, "applications.observe.browser",
					"HH returned an incomplete negotiation", nil,
				)
			}
			if _, duplicate := seen[negotiationID]; duplicate {
				return adapter.ApplicationStateObservationResult{}, operationError(
					core.ErrorTemporaryFailure, "applications.observe.browser",
					"HH negotiations pagination changed; retry observation", nil,
				)
			}
			seen[negotiationID] = struct{}{}
			platformUpdatedAt, err := parseHHTime(topic.LastModified)
			if err != nil {
				return adapter.ApplicationStateObservationResult{}, operationError(
					core.ErrorTemporaryFailure, "applications.observe.browser",
					"HH returned an invalid negotiation update time", err,
				)
			}
			applications = append(applications, adapter.ApplicationStateObservation{
				ExternalNegotiationID: negotiationID, ExternalVacancyID: vacancyID,
				PlatformState: platformState, Disposition: browserApplicationDisposition(platformState),
				ViewedByOpponent: topic.ViewedByOpponent, PlatformUpdatedAt: platformUpdatedAt,
			})
		}
		if pageCount > 0 && page+1 >= pageCount {
			return adapter.ApplicationStateObservationResult{Applications: applications, ObservedAt: observedAt}, nil
		}
	}
	return adapter.ApplicationStateObservationResult{}, operationError(
		core.ErrorTemporaryFailure, "applications.observe.browser",
		"HH negotiations pagination exceeded its safety limit", nil,
	)
}

// browserApplicationDisposition maps the topic state of the web negotiations
// page to the core disposition. INTERVIEW protects an application like an
// invitation: the employer moved it forward and retention must not touch it.
func browserApplicationDisposition(state string) core.ApplicationDisposition {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "RESPONSE":
		return core.ApplicationDispositionPending
	case "INVITATION", "INTERVIEW":
		return core.ApplicationDispositionInvited
	case "DISCARD":
		return core.ApplicationDispositionRejected
	case "HIDDEN":
		return core.ApplicationDispositionHidden
	default:
		return core.ApplicationDispositionUnknown
	}
}
