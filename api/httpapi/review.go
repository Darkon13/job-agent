package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

// ReviewAPI exposes the human fallback of the vacancy test chain. It never
// submits anything to the platform: it records the selection and the
// review.answer worker continues the chain.
type ReviewAPI struct {
	reviews  storage.ReviewRepository
	workflow *workflow.VacancyTestWorkflow
}

func NewReviewAPI(reviews storage.ReviewRepository, workflow *workflow.VacancyTestWorkflow) (*ReviewAPI, error) {
	if reviews == nil || workflow == nil {
		return nil, errors.New("review API requires repository and workflow")
	}
	return &ReviewAPI{reviews: reviews, workflow: workflow}, nil
}

func (api *ReviewAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/review-sessions/{session_id}", api.getSession)
	mux.HandleFunc("POST /api/v1/review-sessions/{session_id}/answers", api.answer)
	mux.Handle("/", next)
	return mux
}

type reviewSessionResponse struct {
	ID               core.ReviewSessionID     `json:"id"`
	TestDefinitionID core.TestDefinitionID    `json:"test_definition_id"`
	Platform         core.Platform            `json:"platform"`
	ProfileID        core.ProfileID           `json:"profile_id"`
	Status           core.ReviewSessionStatus `json:"status"`
	Revision         uint64                   `json:"revision"`
	CreatedAt        time.Time                `json:"created_at"`
	UpdatedAt        time.Time                `json:"updated_at"`
	Prompt           *core.ReviewPrompt       `json:"prompt,omitempty"`
	Selections       []core.ReviewSelection   `json:"selections,omitempty"`
}

func (api *ReviewAPI) getSession(response http.ResponseWriter, request *http.Request) {
	session, err := api.reviews.ReviewSession(request.Context(), core.ReviewSessionID(request.PathValue("session_id")))
	if err != nil {
		writeError(response, err)
		return
	}
	view := reviewSessionResponse{
		ID: session.ID, TestDefinitionID: session.TestDefinitionID, Platform: session.Platform,
		ProfileID: session.ProfileID, Status: session.Status, Revision: session.Revision,
		CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt,
	}
	if prompt, err := api.reviews.ReviewPrompt(request.Context(), reviewPromptID(session)); err == nil {
		view.Prompt = &prompt
	}
	selections, err := api.reviews.ReviewSelections(request.Context(), session.ID)
	if err != nil {
		writeError(response, err)
		return
	}
	view.Selections = selections
	writeJSON(response, http.StatusOK, view)
}

type reviewAnswerRequest struct {
	PromptID         core.ReviewPromptID `json:"prompt_id"`
	ExpectedRevision uint64              `json:"expected_revision"`
	SelectedOptions  []string            `json:"selected_options,omitempty"`
	Text             string              `json:"text,omitempty"`
	Source           string              `json:"source"`
}

func (api *ReviewAPI) answer(response http.ResponseWriter, request *http.Request) {
	key, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	var body reviewAnswerRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	session, err := api.reviews.ReviewSession(request.Context(), core.ReviewSessionID(request.PathValue("session_id")))
	if err != nil {
		writeError(response, err)
		return
	}
	task, created, err := api.workflow.EnqueueReviewAnswer(request.Context(), session, core.ReviewAnswerPayload{
		SessionID: session.ID, PromptID: body.PromptID, ExpectedRevision: body.ExpectedRevision,
		SelectedOptions: body.SelectedOptions, Text: body.Text, Source: body.Source,
	}, key)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, taskResponse{TaskID: task.ID, Created: created})
}

func reviewPromptID(session core.ReviewSession) core.ReviewPromptID {
	return core.ReviewPromptID(fmt.Sprintf("%s-prompt-%d", session.ID, session.Revision))
}
