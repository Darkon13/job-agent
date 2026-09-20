package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

// ReviewAPI exposes the human fallback of the vacancy test chain. It never
// submits anything to the platform: it records the selection and the
// review.answer worker continues the chain.
type ReviewAPI struct {
	reviews   storage.ReviewRepository
	workflow  *workflow.VacancyTestWorkflow
	vacancies reviewVacancyRepository
}

// reviewVacancyRepository resolves the vacancy a vacancy questionnaire belongs
// to. Qualification sessions expose no vacancy and stay without one.
type reviewVacancyRepository interface {
	Vacancy(context.Context, core.VacancyKey) (core.Vacancy, error)
}

func NewReviewAPI(reviews storage.ReviewRepository, workflow *workflow.VacancyTestWorkflow, vacancies reviewVacancyRepository) (*ReviewAPI, error) {
	if reviews == nil || workflow == nil {
		return nil, errors.New("review API requires repository and workflow")
	}
	return &ReviewAPI{reviews: reviews, workflow: workflow, vacancies: vacancies}, nil
}

func (api *ReviewAPI) Handler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/review-sessions", api.listSessions)
	mux.HandleFunc("GET /api/v1/review-sessions/{session_id}", api.getSession)
	mux.HandleFunc("POST /api/v1/review-sessions/{session_id}/answers", api.answer)
	mux.HandleFunc("POST /api/v1/review-sessions/{session_id}/cancel", api.cancel)
	mux.Handle("/", next)
	return mux
}

type reviewVacancyInfo struct {
	ExternalID string `json:"external_id"`
	Title      string `json:"title,omitempty"`
	Employer   string `json:"employer,omitempty"`
	URL        string `json:"url,omitempty"`
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
	Questions        []core.Question          `json:"questions,omitempty"`
	Vacancy          *reviewVacancyInfo       `json:"vacancy,omitempty"`
}

type reviewSessionListItem struct {
	ID               core.ReviewSessionID     `json:"id"`
	TestDefinitionID core.TestDefinitionID    `json:"test_definition_id"`
	Platform         core.Platform            `json:"platform"`
	ProfileID        core.ProfileID           `json:"profile_id"`
	Status           core.ReviewSessionStatus `json:"status"`
	Revision         uint64                   `json:"revision"`
	CreatedAt        time.Time                `json:"created_at"`
	UpdatedAt        time.Time                `json:"updated_at"`
	Question         string                   `json:"question,omitempty"`
	QuestionKind     core.QuestionKind        `json:"question_kind,omitempty"`
	Vacancy          *reviewVacancyInfo       `json:"vacancy,omitempty"`
}

// sessionVacancy attributes a vacancy questionnaire session to its vacancy.
// The external id is still reported when the vacancy row is not (yet) stored.
func (api *ReviewAPI) sessionVacancy(ctx context.Context, session core.ReviewSession) *reviewVacancyInfo {
	if api.vacancies == nil {
		return nil
	}
	externalID, ok := core.VacancyExternalIDFromTestDefinitionID(session.Platform, session.TestDefinitionID)
	if !ok {
		return nil
	}
	info := reviewVacancyInfo{ExternalID: externalID}
	vacancy, err := api.vacancies.Vacancy(ctx, core.VacancyKey{Platform: session.Platform, ExternalID: externalID})
	if err != nil {
		return &info
	}
	info.Title = vacancy.Title
	info.Employer = vacancy.Employer
	info.URL = vacancy.URL
	return &info
}

const (
	reviewSessionDefaultLimit = 50
	reviewSessionMaximumLimit = 200
)

func (api *ReviewAPI) listSessions(response http.ResponseWriter, request *http.Request) {
	filter := storage.ReviewSessionFilter{
		Status:    core.ReviewSessionStatus(strings.TrimSpace(request.URL.Query().Get("status"))),
		ProfileID: core.ProfileID(strings.TrimSpace(request.URL.Query().Get("profile_id"))),
		Platform:  core.Platform(strings.TrimSpace(request.URL.Query().Get("platform"))),
		Query:     strings.TrimSpace(request.URL.Query().Get("q")),
		Limit:     reviewSessionDefaultLimit,
	}
	if filter.Status != "" && !validReviewSessionStatus(filter.Status) {
		writeProblem(response, http.StatusBadRequest, "unsupported review session status")
		return
	}
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > reviewSessionMaximumLimit {
			writeProblem(response, http.StatusBadRequest, "review session limit must be between 1 and 200")
			return
		}
		filter.Limit = limit
	}
	if raw := strings.TrimSpace(request.URL.Query().Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			writeProblem(response, http.StatusBadRequest, "review session offset must not be negative")
			return
		}
		filter.Offset = offset
	}
	sessions, err := api.reviews.ListReviewSessions(request.Context(), filter)
	if err != nil {
		writeError(response, err)
		return
	}
	items := make([]reviewSessionListItem, 0, len(sessions))
	for _, session := range sessions {
		item := reviewSessionListItem{
			ID: session.ID, TestDefinitionID: session.TestDefinitionID, Platform: session.Platform,
			ProfileID: session.ProfileID, Status: session.Status, Revision: session.Revision,
			CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt,
			Vacancy: api.sessionVacancy(request.Context(), session),
		}
		if prompt, err := api.reviews.ReviewPrompt(request.Context(), reviewPromptID(session)); err == nil {
			item.Question = prompt.Question.Text
			item.QuestionKind = prompt.Question.Kind
		}
		items = append(items, item)
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, listResponse[reviewSessionListItem]{Items: items})
}

// cancel hides one session from the operator pool without touching the
// platform: the reviewed answer is not submitted and the session can only be
// seen again through an explicit status filter.
func (api *ReviewAPI) cancel(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireIdempotencyKey(response, request); !ok {
		return
	}
	var body reviewCancelRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	session, err := api.reviews.ReviewSession(request.Context(), core.ReviewSessionID(request.PathValue("session_id")))
	if err != nil {
		writeError(response, err)
		return
	}
	if session.Revision != body.ExpectedRevision {
		writeProblem(response, http.StatusConflict, "review session revision changed")
		return
	}
	switch session.Status {
	case core.ReviewPending, core.ReviewWaiting, core.ReviewAnswered:
	default:
		writeProblem(response, http.StatusConflict, "only an open review session can be cancelled")
		return
	}
	session.Status = core.ReviewCancelled
	session.Revision++
	session.UpdatedAt = time.Now().UTC()
	if err := api.reviews.CancelReviewSession(request.Context(), session, body.ExpectedRevision); err != nil {
		writeError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, reviewSessionListItem{
		ID: session.ID, TestDefinitionID: session.TestDefinitionID, Platform: session.Platform,
		ProfileID: session.ProfileID, Status: session.Status, Revision: session.Revision,
		CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt,
	})
}

type reviewCancelRequest struct {
	ExpectedRevision uint64 `json:"expected_revision"`
}

func validReviewSessionStatus(status core.ReviewSessionStatus) bool {
	switch status {
	case core.ReviewPending, core.ReviewWaiting, core.ReviewAnswered, core.ReviewCompleted,
		core.ReviewCancelled, core.ReviewUnsupported, core.ReviewExpired:
		return true
	default:
		return false
	}
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
		Vacancy: api.sessionVacancy(request.Context(), session),
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
	view.Questions = api.remainingQuestions(request.Context(), session)
	writeJSON(response, http.StatusOK, view)
}

type reviewAnswerRequest struct {
	PromptID         core.ReviewPromptID        `json:"prompt_id"`
	ExpectedRevision uint64                     `json:"expected_revision"`
	SelectedOptions  []string                   `json:"selected_options,omitempty"`
	Text             string                     `json:"text,omitempty"`
	Source           string                     `json:"source"`
	Answers          []reviewBatchAnswerRequest `json:"answers,omitempty"`
	Bank             *bool                      `json:"bank,omitempty"`
}

type reviewBatchAnswerRequest struct {
	QuestionID      string   `json:"question_id"`
	SelectedOptions []string `json:"selected_options,omitempty"`
	Text            string   `json:"text,omitempty"`
	Bank            *bool    `json:"bank,omitempty"`
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
	payload := core.ReviewAnswerPayload{
		SessionID: session.ID, PromptID: body.PromptID, ExpectedRevision: body.ExpectedRevision,
		SelectedOptions: body.SelectedOptions, Text: body.Text, Source: body.Source, Bank: body.Bank,
	}
	for _, answer := range body.Answers {
		payload.Answers = append(payload.Answers, core.ReviewAnswerEntry{
			QuestionID: answer.QuestionID, SelectedOptions: answer.SelectedOptions, Text: answer.Text,
			Bank: answer.Bank,
		})
	}
	task, created, err := api.workflow.EnqueueReviewAnswer(request.Context(), session, payload, key)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, taskResponse{TaskID: task.ID, Created: created})
}

// remainingQuestions lists the observed questionnaire questions that have no
// recorded answer yet. Each answered question left its prompt row behind, so
// the prompt ids below the current revision identify the answered set.
func (api *ReviewAPI) remainingQuestions(ctx context.Context, session core.ReviewSession) []core.Question {
	if len(session.Questionnaire.Questions) == 0 {
		return nil
	}
	answered := make(map[string]struct{}, session.Revision)
	for revision := uint64(1); revision < session.Revision; revision++ {
		promptID := core.ReviewPromptID(fmt.Sprintf("%s-prompt-%d", session.ID, revision))
		prompt, err := api.reviews.ReviewPrompt(ctx, promptID)
		if err != nil {
			continue
		}
		answered[prompt.Question.ID] = struct{}{}
	}
	remaining := make([]core.Question, 0, len(session.Questionnaire.Questions))
	for _, question := range session.Questionnaire.Questions {
		if _, ok := answered[question.ID]; !ok {
			remaining = append(remaining, question)
		}
	}
	return remaining
}

func reviewPromptID(session core.ReviewSession) core.ReviewPromptID {
	return core.ReviewPromptID(fmt.Sprintf("%s-prompt-%d", session.ID, session.Revision))
}
