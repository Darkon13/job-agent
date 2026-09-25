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
			// Merged historical applications may reference a vacancy that is not
			// in the local catalogue. Keep the row visible with empty vacancy
			// fields instead of failing the whole page.
			vacancy = core.Vacancy{}
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
		if tailorings, ok := api.repository.(interface {
			ApplicationTailoringByApplication(context.Context, core.ApplicationID) (core.ApplicationTailoring, error)
		}); ok {
			if tailoring, tailoringErr := tailorings.ApplicationTailoringByApplication(request.Context(), application.ID); tailoringErr == nil {
				if changes, changeErr := tailoring.RedactedChanges(); changeErr == nil {
					summary.Tailoring = &ApplicationTailoringSummary{
						Status: tailoring.Status, ProcessorTag: tailoring.ProcessorTag,
						ProcessorVersion: tailoring.ProcessorVersion, RecoveryReason: tailoring.RecoveryReason,
						Changes: changes, UpdatedAt: tailoring.UpdatedAt,
					}
				}
			}
		}
		items = append(items, summary)
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, applicationListResponse{Items: items, Total: page.Total, Groups: page.Groups, Offset: filter.Offset, Limit: filter.Limit})
}

type ApplicationAPI struct {
	removal      *workflow.ApplicationRemovalWorkflow
	retry        *workflow.ApplicationRetryWorkflow
	browserCheck browserCheckController
}

func NewApplicationAPI(removal *workflow.ApplicationRemovalWorkflow, retry *workflow.ApplicationRetryWorkflow) (*ApplicationAPI, error) {
	if removal == nil || retry == nil {
		return nil, errors.New("application API requires removal and retry workflows")
	}
	return &ApplicationAPI{removal: removal, retry: retry}, nil
}

// ConfigureBrowserCheck attaches the interactive browser check controller.
// Without it the browser check routes report that the feature is unavailable.
func (api *ApplicationAPI) ConfigureBrowserCheck(controller browserCheckController) {
	api.browserCheck = controller
}

func (api *ApplicationAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/applications/remove", api.remove)
	mux.HandleFunc("POST /api/v1/applications/{application_id}/retry", api.retryApplication)
	mux.HandleFunc("POST /api/v1/applications/{application_id}/browser-check", api.startBrowserCheck)
	mux.HandleFunc("GET /api/v1/applications/{application_id}/browser-check/{session_id}", api.browserCheckStatus)
	mux.HandleFunc("GET /api/v1/applications/{application_id}/browser-check/{session_id}/image", api.browserCheckImage)
	mux.HandleFunc("POST /api/v1/applications/{application_id}/browser-check/{session_id}/answer", api.answerBrowserCheck)
	mux.HandleFunc("DELETE /api/v1/applications/{application_id}/browser-check/{session_id}", api.cancelBrowserCheck)
	mux.Handle("/", next)
	return mux
}

// retryApplication releases one blocked application: it clears the recorded
// decision and lets the durable submit workflow re-read the platform state.
func (api *ApplicationAPI) retryApplication(response http.ResponseWriter, request *http.Request) {
	if !emptyRequestBody(response, request) {
		return
	}
	requestKey, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	applicationID := core.ApplicationID(strings.TrimSpace(request.PathValue("application_id")))
	task, created, err := api.retry.Enqueue(request.Context(), applicationID, requestKey)
	if err != nil {
		writeError(response, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, status, taskResponse{TaskID: task.ID, Created: created})
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

// captureQuestionnaire enqueues the read-only capture of the vacancy
// questionnaire behind an application. The review section answers it in the
// dashboard; nothing is submitted by this request.
func (api *RuntimeAPI) captureQuestionnaire(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireIdempotencyKey(response, request); !ok {
		return
	}
	if api.questionnaires == nil {
		writeProblem(response, http.StatusServiceUnavailable, "questionnaire capture is not configured")
		return
	}
	applicationID := core.ApplicationID(strings.TrimSpace(request.PathValue("application_id")))
	application, err := api.repository.ApplicationByID(request.Context(), applicationID)
	if err != nil {
		writeProblem(response, http.StatusNotFound, "application not found")
		return
	}
	created, err := api.questionnaires.EnqueueCapture(
		request.Context(), application.Key.ProfileID, application.Key.Vacancy.Platform,
		application.Key.Vacancy.ExternalID, "dashboard-questionnaire",
	)
	if err != nil {
		writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusAccepted, map[string]bool{"created": created})
}
