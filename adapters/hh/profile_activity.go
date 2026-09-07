package hh

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

var _ adapter.ProfileActivityObserver = (*ResumeTouchTransport)(nil)

type profileActivityCounter struct {
	Count    *int `json:"count"`
	CountNew *int `json:"countNew"`
}

type profileResumeStatistics struct {
	Statistics struct {
		PeriodDays  *int                    `json:"periodDays"`
		SearchShows *profileActivityCounter `json:"searchShows"`
		Views       *profileActivityCounter `json:"views"`
		Invitations *profileActivityCounter `json:"invitations"`
	} `json:"statistics"`
}

// ObserveProfileActivity performs one read-only profile GET. It reads only
// counters already present in the profile initial state and never opens a
// vacancy or emits browser telemetry.
func (transport *ResumeTouchTransport) ObserveProfileActivity(ctx context.Context, profileID core.ProfileID, resumeID string) (adapter.ProfileActivityObservation, error) {
	if profileID == "" || strings.TrimSpace(resumeID) == "" {
		return adapter.ProfileActivityObservation{}, fmt.Errorf("HH profile activity observation requires profile and resume")
	}
	client, err := transport.authenticatedClient()
	if err != nil {
		return adapter.ProfileActivityObservation{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, transport.profileURL, nil)
	if err != nil {
		return adapter.ProfileActivityObservation{}, err
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
	response, err := client.Do(request)
	if err != nil {
		return adapter.ProfileActivityObservation{}, &core.OperationError{Category: core.ErrorTemporaryFailure, Operation: "activity.observe", Platform: Name, Message: "profile activity request failed", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return adapter.ProfileActivityObservation{}, &core.OperationError{Category: core.ErrorUnauthorized, Operation: "activity.observe", Platform: Name, Message: "browser session is unauthorized"}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return adapter.ProfileActivityObservation{}, err
	}
	match := initialStatePattern.FindSubmatch(data)
	if len(match) != 2 {
		return adapter.ProfileActivityObservation{}, &core.OperationError{
			Category: core.ErrorTemporaryFailure, Operation: "activity.observe", Platform: Name,
			Message:  "HH profile activity state was not found",
			Metadata: map[string]string{"status": fmt.Sprint(response.StatusCode), "final_path": response.Request.URL.Path},
		}
	}
	decoded := []byte(html.UnescapeString(string(match[1])))
	var state struct {
		ApplicantResumes []struct {
			Attributes struct {
				ID   string `json:"id"`
				Hash string `json:"hash"`
			} `json:"_attributes"`
		} `json:"applicantResumes"`
		ApplicantResumesStatistics struct {
			Recommendations struct {
				ResponsesCount    *int `json:"responsesCount"`
				ResponsesRequired *int `json:"responsesRequired"`
			} `json:"recommendationsForAllResumes"`
			Resumes map[string]profileResumeStatistics `json:"resumes"`
		} `json:"applicantResumesStatistics"`
		Experiments struct {
			Enabled map[string]any `json:"enabled"`
		} `json:"experiments"`
	}
	if err := json.Unmarshal(decoded, &state); err != nil {
		return adapter.ProfileActivityObservation{}, fmt.Errorf("decode HH profile activity state: %w", err)
	}
	statisticsKey := strings.TrimSpace(resumeID)
	for _, resume := range state.ApplicantResumes {
		if resume.Attributes.ID == resumeID || resume.Attributes.Hash == resumeID {
			statisticsKey = resume.Attributes.ID
			break
		}
	}
	statistics, exists := state.ApplicantResumesStatistics.Resumes[statisticsKey]
	if !exists {
		return adapter.ProfileActivityObservation{}, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "activity.observe", Platform: Name,
			Message: "configured resume activity statistics were not found",
		}
	}
	var generic any
	_ = json.Unmarshal(decoded, &generic)
	result := adapter.ProfileActivityObservation{
		Score:             findActivityScore(generic),
		ScoreHidden:       experimentEnabled(state.Experiments.Enabled["web_hide_user_activity"]),
		PeriodDays:        statistics.Statistics.PeriodDays,
		ResponseStreak:    state.ApplicantResumesStatistics.Recommendations.ResponsesCount,
		ResponsesRequired: state.ApplicantResumesStatistics.Recommendations.ResponsesRequired,
		ObservedAt:        time.Now().UTC(),
	}
	if statistics.Statistics.SearchShows != nil {
		result.SearchShows = statistics.Statistics.SearchShows.Count
	}
	if statistics.Statistics.Views != nil {
		result.Views = statistics.Statistics.Views.Count
		result.NewViews = statistics.Statistics.Views.CountNew
	}
	if statistics.Statistics.Invitations != nil {
		result.Invitations = statistics.Statistics.Invitations.Count
		result.NewInvitations = statistics.Statistics.Invitations.CountNew
	}
	return result, nil
}

func experimentEnabled(value any) bool {
	switch item := value.(type) {
	case string:
		return item != "" && item != "control"
	case bool:
		return item
	default:
		return false
	}
}

func findActivityScore(value any) *int {
	return findNestedActivityScore(value, false)
}

func findNestedActivityScore(value any, activityParent bool) *int {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			lower := strings.ToLower(key)
			insideActivity := activityParent || strings.Contains(lower, "activity")
			if lower == "activityscore" || lower == "activity_score" || (activityParent && lower == "score") {
				if number, ok := child.(float64); ok && number >= 0 && number == float64(int(number)) {
					result := int(number)
					return &result
				}
			}
			if result := findNestedActivityScore(child, insideActivity); result != nil {
				return result
			}
		}
	case []any:
		for _, child := range item {
			if result := findNestedActivityScore(child, activityParent); result != nil {
				return result
			}
		}
	}
	return nil
}
