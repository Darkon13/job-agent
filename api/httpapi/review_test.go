package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
	"github.com/Darkon13/job-agent/workflow"
)

func newReviewAPI(t *testing.T) (http.Handler, *storagememory.Repository) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	definition, err := core.NewProgressiveTestDefinition("hh", "vacancy:42", "Go basics", nil, now)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if _, err := repository.UpsertTestDefinition(ctx, definition); err != nil {
		t.Fatalf("store definition: %v", err)
	}
	session, err := core.NewReviewSession("review-1", definition, "profile-1", "correlation-1", now)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	session.Questionnaire = core.Questionnaire{
		Title: "Go basics",
		Questions: []core.Question{
			{ID: "1", Text: "Pick a language", Kind: core.QuestionSingle, Options: []core.QuestionOption{
				{ID: "10", Text: "Go"}, {ID: "11", Text: "Python"},
			}},
		},
	}
	if _, err := repository.CreateReviewSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	prompt := core.ReviewPrompt{
		ID: "review-1-prompt-1", SessionID: session.ID, Revision: 1,
		Question: session.Questionnaire.Questions[0], CreatedAt: now,
	}
	if err := session.WaitForAnswer(prompt, now); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := repository.SaveReviewPrompt(ctx, session, prompt, 1); err != nil {
		t.Fatalf("save prompt: %v", err)
	}
	queue := brokermemory.NewQueue()
	workflow, err := workflow.NewVacancyTestWorkflow(queue, &apiClock{now: now}, &apiIDs{})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	api, err := NewReviewAPI(repository, workflow)
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	return api.Handler(nil), repository
}

func TestReviewAPIExposesWaitingPrompt(t *testing.T) {
	handler, _ := newReviewAPI(t)
	response := performRequest(t, handler, http.MethodGet, "/api/v1/review-sessions/review-1", "", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body reviewSessionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != core.ReviewWaiting || body.Revision != 1 || body.Prompt == nil || body.Prompt.Question.Text != "Pick a language" {
		t.Fatalf("response = %#v", body)
	}
}

func TestReviewAPIEnqueuesIdempotentAnswer(t *testing.T) {
	handler, _ := newReviewAPI(t)
	body := reviewAnswerRequest{
		PromptID: "review-1-prompt-1", ExpectedRevision: 1,
		SelectedOptions: []string{"Go"}, Source: "cli",
	}
	response := performRequest(t, handler, http.MethodPost, "/api/v1/review-sessions/review-1/answers", "request-1", "", body)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var first taskResponse
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if first.TaskID == "" || !first.Created {
		t.Fatalf("first = %#v", first)
	}
	replay := performRequest(t, handler, http.MethodPost, "/api/v1/review-sessions/review-1/answers", "request-1", "", body)
	var second taskResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if second.Created || second.TaskID != first.TaskID {
		t.Fatalf("replay = %#v", second)
	}

	missingKey := performRequest(t, handler, http.MethodPost, "/api/v1/review-sessions/review-1/answers", "", "", body)
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing key status = %d", missingKey.Code)
	}
	invalid := performRequest(t, handler, http.MethodPost, "/api/v1/review-sessions/review-1/answers", "request-2", "", reviewAnswerRequest{
		PromptID: "review-1-prompt-1", ExpectedRevision: 1, Source: "cli",
	})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid body status = %d body=%s", invalid.Code, invalid.Body.String())
	}
}
