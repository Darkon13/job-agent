package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

const maxBulkApplicationAction = 200

type applicationListResponse struct {
	Items  []ApplicationSummary `json:"items"`
	Total  int                  `json:"total"`
	Groups map[string]int       `json:"groups"`
	Offset int                  `json:"offset"`
	Limit  int                  `json:"limit"`
}

func (api *RuntimeAPI) listApplications(response http.ResponseWriter, request *http.Request) {
	limit := 100
	if value := strings.TrimSpace(request.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeProblem(response, http.StatusBadRequest, "application limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	filter := storage.ApplicationQuery{
		ProfileID: core.ProfileID(strings.TrimSpace(request.URL.Query().Get("profile_id"))),
		Status:    core.ApplicationStatus(strings.TrimSpace(request.URL.Query().Get("status"))),
		Limit:     limit,
		Query:     request.URL.Query().Get("q"), Employer: request.URL.Query().Get("employer"),
		Group: request.URL.Query().Get("group"), Sort: request.URL.Query().Get("sort"),
	}
	if value := request.URL.Query().Get("offset"); value != "" {
		offset, err := strconv.Atoi(value)
		if err != nil || offset < 0 {
			writeProblem(response, http.StatusBadRequest, "invalid application offset")
			return
		}
		filter.Offset = offset
	}
	if err := filter.Validate(); err != nil {
		writeProblem(response, http.StatusBadRequest, err.Error())
		return
	}
	page, err := api.repository.QueryApplications(request.Context(), filter)
	if err != nil {
		writeProblem(response, http.StatusInternalServerError, "load applications")
		return
	}
	items := make([]ApplicationSummary, 0, len(page.IDs))
	for _, id := range page.IDs {
		application, err := api.repository.ApplicationByID(request.Context(), id)
		if err != nil {
			writeProblem(response, http.StatusConflict, "application list changed; refresh")
			return
		}
		vacancy, err := api.repository.Vacancy(request.Context(), application.Key.Vacancy)
		if err != nil {
			writeProblem(response, http.StatusInternalServerError, "load application vacancy")
			return
		}
		summary := ApplicationSummary{
			ID: application.ID, Platform: application.Key.Vacancy.Platform, ProfileID: application.Key.ProfileID,
			Status: application.Status, DecisionCode: application.DecisionCode,
			FailureCategory: application.FailureCategory, Attempts: application.Attempts,
			VacancyTitle: vacancy.Title, Employer: vacancy.Employer, VacancyURL: vacancy.URL,
			HasCoverLetter: strings.TrimSpace(application.PreparedMessage) != "",
			UpdatedAt:      application.UpdatedAt, SubmittedAt: application.SubmittedAt,
		}
		if states, ok := api.repository.(interface {
			ApplicationPlatformState(context.Context, core.ApplicationID) (core.ApplicationPlatformState, error)
		}); ok {
			if state, stateErr := states.ApplicationPlatformState(request.Context(), application.ID); stateErr == nil {
				summary.PlatformState = state.PlatformState
				summary.Disposition = state.Disposition
				summary.ViewedByOpponent = state.ViewedByOpponent
				observedAt := state.ObservedAt
				summary.PlatformObservedAt = &observedAt
			}
		}
		items = append(items, summary)
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, applicationListResponse{Items: items, Total: page.Total, Groups: page.Groups, Offset: filter.Offset, Limit: filter.Limit})
}

type ApplicationAPI struct {
	removal *workflow.ApplicationRemovalWorkflow
}

func NewApplicationAPI(removal *workflow.ApplicationRemovalWorkflow) (*ApplicationAPI, error) {
	if removal == nil {
		return nil, errors.New("application API requires removal workflow")
	}
	return &ApplicationAPI{removal: removal}, nil
}

func (api *ApplicationAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/applications/remove", api.remove)
	mux.Handle("/", next)
	return mux
}

type removeApplicationsRequest struct {
	ApplicationIDs []core.ApplicationID `json:"application_ids"`
}

type applicationActionResult struct {
	ApplicationID core.ApplicationID `json:"application_id"`
	TaskID        core.TaskID        `json:"task_id,omitempty"`
	Error         string             `json:"error,omitempty"`
}

type applicationBulkResult struct {
	bulkTaskResponse
	Results []applicationActionResult `json:"results"`
}

func (api *ApplicationAPI) remove(response http.ResponseWriter, request *http.Request) {
	requestKey, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	var body removeApplicationsRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	if len(body.ApplicationIDs) == 0 || len(body.ApplicationIDs) > maxBulkApplicationAction {
		writeProblem(response, http.StatusBadRequest, "application_ids must contain between 1 and 200 objects")
		return
	}
	unique := make([]core.ApplicationID, 0, len(body.ApplicationIDs))
	seen := make(map[core.ApplicationID]struct{}, len(body.ApplicationIDs))
	for _, applicationID := range body.ApplicationIDs {
		applicationID = core.ApplicationID(strings.TrimSpace(string(applicationID)))
		if applicationID == "" {
			writeProblem(response, http.StatusBadRequest, "application_ids must not contain empty ids")
			return
		}
		if _, duplicate := seen[applicationID]; duplicate {
			continue
		}
		seen[applicationID] = struct{}{}
		unique = append(unique, applicationID)
	}
	result := applicationBulkResult{bulkTaskResponse: bulkTaskResponse{Tasks: make([]taskResponse, 0, len(unique)), Matched: len(unique)}, Results: make([]applicationActionResult, 0, len(unique))}
	for _, applicationID := range unique {
		task, created, err := api.removal.Enqueue(request.Context(), applicationID, requestKey)
		if err != nil {
			result.Results = append(result.Results, applicationActionResult{ApplicationID: applicationID, Error: "not_enqueued"})
			continue
		}
		if created {
			result.Created++
		}
		result.Tasks = append(result.Tasks, taskResponse{TaskID: task.ID, Created: created})
		result.Results = append(result.Results, applicationActionResult{ApplicationID: applicationID, TaskID: task.ID})
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusAccepted, result)
}
