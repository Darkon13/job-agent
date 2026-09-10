package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	"github.com/Darkon13/job-agent/workflow"
)

type ConversationAPI struct {
	repository storage.ConversationRepository
	workflow   *workflow.ConversationWorkflow
}

func NewConversationAPI(repository storage.ConversationRepository, conversationWorkflow *workflow.ConversationWorkflow) (*ConversationAPI, error) {
	if repository == nil || conversationWorkflow == nil {
		return nil, errors.New("conversation API requires repository and workflow")
	}
	return &ConversationAPI{repository: repository, workflow: conversationWorkflow}, nil
}

func (api *ConversationAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/profiles/{profile_id}/conversations", api.listConversations)
	mux.HandleFunc("GET /api/v1/conversations/{conversation_id}", api.getConversation)
	mux.HandleFunc("POST /api/v1/conversations/{conversation_id}/sync", api.syncConversation)
	mux.HandleFunc("GET /api/v1/conversations/{conversation_id}/messages", api.listMessages)
	mux.HandleFunc("POST /api/v1/conversations/{conversation_id}/messages", api.sendMessage)
	mux.HandleFunc("POST /api/v1/conversations/mark-read", api.markAllRead)
	mux.HandleFunc("POST /api/v1/conversations/{conversation_id}/mark-read", api.markRead)
	mux.HandleFunc("GET /api/v1/conversations/{conversation_id}/follow-ups", api.listFollowUps)
	mux.HandleFunc("POST /api/v1/conversations/{conversation_id}/follow-ups", api.scheduleFollowUp)
	mux.HandleFunc("GET /api/v1/follow-ups/{follow_up_id}", api.getFollowUp)
	mux.HandleFunc("PATCH /api/v1/follow-ups/{follow_up_id}", api.rescheduleFollowUp)
	mux.HandleFunc("DELETE /api/v1/follow-ups/{follow_up_id}", api.cancelFollowUp)
	mux.HandleFunc("POST /api/v1/follow-ups/{follow_up_id}/run", api.runFollowUp)
	return mux
}

func (api *ConversationAPI) listConversations(response http.ResponseWriter, request *http.Request) {
	items, err := api.repository.ListConversations(request.Context(), storage.ConversationFilter{
		ProfileID: core.ProfileID(request.PathValue("profile_id")),
	})
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, listResponse[core.Conversation]{Items: items})
}

func (api *ConversationAPI) getConversation(response http.ResponseWriter, request *http.Request) {
	conversation, err := api.repository.Conversation(request.Context(), core.ConversationID(request.PathValue("conversation_id")))
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, conversation)
}

func (api *ConversationAPI) listMessages(response http.ResponseWriter, request *http.Request) {
	messages, err := api.repository.ConversationMessages(request.Context(), core.ConversationID(request.PathValue("conversation_id")))
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, listResponse[core.ConversationMessage]{Items: messages})
}

type sendMessageRequest struct {
	ReplyToID core.MessageID      `json:"reply_to_id,omitempty"`
	Content   core.MessageContent `json:"content"`
}

func (api *ConversationAPI) sendMessage(response http.ResponseWriter, request *http.Request) {
	key, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	var body sendMessageRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	task, created, err := api.workflow.EnqueueMessage(request.Context(),
		core.ConversationID(request.PathValue("conversation_id")), body.Content, body.ReplyToID, key)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, taskResponse{TaskID: task.ID, Created: created})
}

func (api *ConversationAPI) markRead(response http.ResponseWriter, request *http.Request) {
	key, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	task, created, err := api.workflow.EnqueueMarkRead(request.Context(), core.ConversationID(request.PathValue("conversation_id")), key)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, taskResponse{TaskID: task.ID, Created: created})
}

func (api *ConversationAPI) markAllRead(response http.ResponseWriter, request *http.Request) {
	key, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	conversations, err := api.repository.ListConversations(request.Context(), storage.ConversationFilter{
		ProfileID: core.ProfileID(strings.TrimSpace(request.URL.Query().Get("profile_id"))),
		Status:    core.ConversationActive,
	})
	if err != nil {
		writeError(response, err)
		return
	}
	result := bulkTaskResponse{Tasks: make([]taskResponse, 0)}
	for _, conversation := range conversations {
		if conversation.UnreadCount == 0 {
			continue
		}
		result.Matched++
		task, created, err := api.workflow.EnqueueMarkRead(request.Context(), conversation.ID, key)
		if err != nil {
			writeError(response, err)
			return
		}
		if created {
			result.Created++
		}
		result.Tasks = append(result.Tasks, taskResponse{TaskID: task.ID, Created: created})
	}
	writeJSON(response, http.StatusAccepted, result)
}

func (api *ConversationAPI) syncConversation(response http.ResponseWriter, request *http.Request) {
	key, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	task, created, err := api.workflow.EnqueueSync(request.Context(), core.ConversationID(request.PathValue("conversation_id")), key)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, taskResponse{TaskID: task.ID, Created: created})
}

type scheduleFollowUpRequest struct {
	AnchorMessageID core.MessageID      `json:"anchor_message_id"`
	AnchorAt        time.Time           `json:"anchor_at"`
	RunAt           time.Time           `json:"run_at"`
	Deadline        *time.Time          `json:"deadline,omitempty"`
	Content         core.MessageContent `json:"content"`
	Policy          core.FollowUpPolicy `json:"policy"`
}

func (api *ConversationAPI) scheduleFollowUp(response http.ResponseWriter, request *http.Request) {
	key, ok := requireIdempotencyKey(response, request)
	if !ok {
		return
	}
	var body scheduleFollowUpRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	followUp, created, err := api.workflow.ScheduleFollowUp(request.Context(), workflow.ScheduleFollowUpRequest{
		ConversationID:  core.ConversationID(request.PathValue("conversation_id")),
		AnchorMessageID: body.AnchorMessageID, AnchorAt: body.AnchorAt, RunAt: body.RunAt,
		Deadline: body.Deadline, Content: body.Content, Policy: body.Policy, IdempotencyKey: key,
	})
	if err != nil {
		writeError(response, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(response, status, followUp)
}

func (api *ConversationAPI) listFollowUps(response http.ResponseWriter, request *http.Request) {
	items, err := api.repository.ListFollowUps(request.Context(), storage.FollowUpFilter{
		ConversationID: core.ConversationID(request.PathValue("conversation_id")),
	})
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, listResponse[core.FollowUp]{Items: items})
}

func (api *ConversationAPI) getFollowUp(response http.ResponseWriter, request *http.Request) {
	followUp, err := api.repository.FollowUp(request.Context(), core.FollowUpID(request.PathValue("follow_up_id")))
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, followUp)
}

type rescheduleFollowUpRequest struct {
	RunAt    time.Time  `json:"run_at"`
	Deadline *time.Time `json:"deadline,omitempty"`
}

func (api *ConversationAPI) rescheduleFollowUp(response http.ResponseWriter, request *http.Request) {
	revision, ok := requireRevision(response, request)
	if !ok {
		return
	}
	var body rescheduleFollowUpRequest
	if !decodeJSON(response, request, &body) {
		return
	}
	followUp, err := api.workflow.RescheduleFollowUp(request.Context(), core.FollowUpID(request.PathValue("follow_up_id")), body.RunAt, body.Deadline, revision)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, followUp)
}

func (api *ConversationAPI) cancelFollowUp(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireIdempotencyKey(response, request); !ok {
		return
	}
	followUp, err := api.workflow.CancelFollowUp(request.Context(), core.FollowUpID(request.PathValue("follow_up_id")), core.FollowUpUserRequested)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, followUp)
}

func (api *ConversationAPI) runFollowUp(response http.ResponseWriter, request *http.Request) {
	if _, ok := requireIdempotencyKey(response, request); !ok {
		return
	}
	revision, ok := requireRevision(response, request)
	if !ok {
		return
	}
	followUp, taskCreated, err := api.workflow.RunFollowUpNow(request.Context(), core.FollowUpID(request.PathValue("follow_up_id")), revision)
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, struct {
		FollowUp    core.FollowUp `json:"follow_up"`
		TaskCreated bool          `json:"task_created"`
	}{FollowUp: followUp, TaskCreated: taskCreated})
}

type listResponse[T any] struct {
	Items []T `json:"items"`
}

type taskResponse struct {
	TaskID  core.TaskID `json:"task_id"`
	Created bool        `json:"created"`
}

type bulkTaskResponse struct {
	Tasks   []taskResponse `json:"tasks"`
	Matched int            `json:"matched"`
	Created int            `json:"created"`
}

func requireIdempotencyKey(response http.ResponseWriter, request *http.Request) (string, bool) {
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		writeProblem(response, http.StatusBadRequest, "Idempotency-Key header is required")
		return "", false
	}
	return key, true
}

func requireRevision(response http.ResponseWriter, request *http.Request) (uint64, bool) {
	value := strings.Trim(strings.TrimSpace(request.Header.Get("If-Match")), `"`)
	revision, err := strconv.ParseUint(value, 10, 64)
	if err != nil || revision == 0 {
		writeProblem(response, http.StatusBadRequest, "If-Match must contain a positive revision")
		return 0, false
	}
	return revision, true
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeProblem(response, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			writeProblem(response, http.StatusBadRequest, "request body must contain one JSON value")
		} else {
			writeProblem(response, http.StatusBadRequest, fmt.Sprintf("invalid trailing JSON: %v", err))
		}
		return false
	}
	return true
}

func writeError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrRevisionConflict):
		writeProblem(response, http.StatusConflict, err.Error())
	case errors.Is(err, sql.ErrNoRows), strings.Contains(strings.ToLower(err.Error()), "not found"):
		writeProblem(response, http.StatusNotFound, err.Error())
	default:
		writeProblem(response, http.StatusBadRequest, err.Error())
	}
}

func writeProblem(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, struct {
		Error string `json:"error"`
	}{Error: message})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
