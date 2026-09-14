package hh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Darkon13/job-agent/core"
)

func TestObserveProfileActivityReadsCurrentCountersAndHiddenFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		state := map[string]any{
			"applicantResumes": []any{map[string]any{"_attributes": map[string]any{"id": "42", "hash": "resume-hash"}}},
			"applicantResumesStatistics": map[string]any{
				"recommendationsForAllResumes": map[string]any{"responsesCount": 2, "responsesRequired": 10},
				"resumes": map[string]any{"42": map[string]any{"statistics": map[string]any{
					"periodDays": 7, "searchShows": map[string]any{"count": 35},
					"views":       map[string]any{"count": 1, "countNew": 1},
					"invitations": map[string]any{"count": 0, "countNew": 0},
				}}},
			},
			"experiments": map[string]any{"enabled": map[string]any{"web_hide_user_activity": "experiment"}},
		}
		encoded, _ := json.Marshal(state)
		_, _ = response.Write([]byte(`<template class="ResumeProfileFront-InitialState">` + string(encoded) + `</template>`))
	}))
	t.Cleanup(server.Close)
	statePath := filepath.Join(t.TempDir(), "state.json")
	data, _ := json.Marshal(browserStorageState{})
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	transport, err := NewResumeTouchTransport(statePath, server.Client())
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	transport.profileURL = server.URL
	observation, err := transport.ObserveProfileActivity(context.Background(), core.ProfileID("primary"), "resume-hash")
	if err != nil {
		t.Fatalf("observe activity: %v", err)
	}
	if !observation.ScoreHidden || observation.Score != nil || observation.PeriodDays == nil || *observation.PeriodDays != 7 ||
		observation.SearchShows == nil || *observation.SearchShows != 35 || observation.Views == nil || *observation.Views != 1 ||
		observation.Invitations == nil || *observation.Invitations != 0 || observation.ResponseStreak == nil || *observation.ResponseStreak != 2 ||
		observation.ResponsesRequired == nil || *observation.ResponsesRequired != 10 || observation.ObservedAt.IsZero() {
		t.Fatalf("unexpected activity observation: %#v", observation)
	}
}

func TestFindActivityScoreIgnoresUnrelatedSkillScores(t *testing.T) {
	value := map[string]any{
		"skills":            map[string]any{"score": float64(9)},
		"applicantActivity": map[string]any{"score": float64(73)},
	}
	score := findActivityScore(value)
	if score == nil || *score != 73 {
		t.Fatalf("activity score=%v", score)
	}
}

func TestObserveProfileActivityPrefersRootActivityWidget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/" {
			lux := map[string]any{
				"applicantActivity": map[string]any{"userActivityScore": 100, "userActivityScoreChange": 0, "showActivity": true},
			}
			encoded, _ := json.Marshal(lux)
			_, _ = response.Write([]byte(`<template id="HH-Lux-InitialState">` + string(encoded) + `</template>`))
			return
		}
		state := map[string]any{
			"applicantResumes": []any{map[string]any{"_attributes": map[string]any{"id": "42", "hash": "resume-hash"}}},
			"applicantResumesStatistics": map[string]any{
				"recommendationsForAllResumes": map[string]any{"responsesCount": 2, "responsesRequired": 10},
				"resumes": map[string]any{"42": map[string]any{"statistics": map[string]any{
					"periodDays": 7, "searchShows": map[string]any{"count": 35},
					"views":       map[string]any{"count": 1, "countNew": 1},
					"invitations": map[string]any{"count": 0, "countNew": 0},
				}}},
			},
			"experiments": map[string]any{"enabled": map[string]any{"web_hide_user_activity": "experiment"}},
		}
		encoded, _ := json.Marshal(state)
		_, _ = response.Write([]byte(`<template class="ResumeProfileFront-InitialState">` + string(encoded) + `</template>`))
	}))
	defer server.Close()
	statePath := filepath.Join(t.TempDir(), "state.json")
	data, _ := json.Marshal(browserStorageState{Cookies: []browserCookie{{Name: "session", Value: "ready", Path: "/"}}})
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	transport, err := NewResumeTouchTransport(statePath, server.Client())
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	transport.profileURL = server.URL + "/applicant/resumes"
	observation, err := transport.ObserveProfileActivity(context.Background(), "primary", "42")
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observation.Score == nil || *observation.Score != 100 || observation.ScoreHidden {
		t.Fatalf("root activity widget was not used: %#v", observation)
	}
}
