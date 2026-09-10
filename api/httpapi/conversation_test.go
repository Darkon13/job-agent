package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
	"github.com/Darkon13/job-agent/workflow"
)

type apiClock struct{ now time.Time }

func (clock *apiClock) Now() time.Time { return clock.now }

type apiIDs struct{ next int }

func (ids *apiIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}

func newAPI(t *testing.T) (http.Handler, *brokermemory.Queue, *apiClock) {
	t.Helper()
	repository := storagememory.NewRepository()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	conversation, err := core.NewConversation("conversation-1", "hh", "profile-1", "external-chat-1", now)
	if err != nil {
		t.Fatalf("new conversation: %v", err)
	}
	conversation.UnreadCount = 2
	if _, _, err := repository.CreateConversation(context.Background(), conversation); err != nil {
		t.Fatalf("store conversation: %v", err)
	}
	queue := brokermemory.NewQueue()
	clock := &apiClock{now: now}
	conversationWorkflow, err := workflow.NewConversationWorkflow(repository, queue, clock, &apiIDs{})
	if err != nil {
		t.Fatalf("new workflow: %v", err)
	}
	api, err := NewConversationAPI(repository, conversationWorkflow)
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	return api.Handler(), queue, clock
}

func performRequest(t *testing.T, handler http.Handler, method, path, idempotencyKey, revision string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if revision != "" {
		request.Header.Set("If-Match", revision)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestConversationMessageAPIRequiresAndDeduplicatesIdempotencyKey(t *testing.T) {
	handler, queue, _ := newAPI(t)
	body := sendMessageRequest{Content: core.MessageContent{Text: "Здравствуйте"}}
	response := performRequest(t, handler, http.MethodPost, "/api/v1/conversations/conversation-1/messages", "", "", body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency status: %d body=%s", response.Code, response.Body.String())
	}
	response = performRequest(t, handler, http.MethodPost, "/api/v1/conversations/conversation-1/messages", "send-1", "", body)
	if response.Code != http.StatusAccepted {
		t.Fatalf("send status: %d body=%s", response.Code, response.Body.String())
	}
	var first taskResponse
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil || !first.Created {
		t.Fatalf("send response: %#v err=%v", first, err)
	}
	response = performRequest(t, handler, http.MethodPost, "/api/v1/conversations/conversation-1/messages", "send-1", "", body)
	var repeated taskResponse
	if err := json.Unmarshal(response.Body.Bytes(), &repeated); err != nil || repeated.Created || repeated.TaskID != first.TaskID || len(queue.Tasks()) != 1 {
		t.Fatalf("repeated response: %#v tasks=%d err=%v", repeated, len(queue.Tasks()), err)
	}
}

func TestConversationAPIMarksAllUnreadDialogsIdempotently(t *testing.T) {
	handler, queue, _ := newAPI(t)
	response := performRequest(t, handler, http.MethodPost, "/api/v1/conversations/mark-read", "mark-all-1", "", nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("bulk mark-read status: %d body=%s", response.Code, response.Body.String())
	}
	var first bulkTaskResponse
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil || first.Matched != 1 || first.Created != 1 || len(first.Tasks) != 1 || len(queue.Tasks()) != 1 {
		t.Fatalf("bulk mark-read response=%#v tasks=%d err=%v", first, len(queue.Tasks()), err)
	}
	response = performRequest(t, handler, http.MethodPost, "/api/v1/conversations/mark-read", "mark-all-1", "", nil)
	var repeated bulkTaskResponse
	if err := json.Unmarshal(response.Body.Bytes(), &repeated); err != nil || repeated.Matched != 1 || repeated.Created != 0 || len(queue.Tasks()) != 1 {
		t.Fatalf("repeated bulk mark-read response=%#v tasks=%d err=%v", repeated, len(queue.Tasks()), err)
	}
}

func TestFollowUpAPISupportsSchedulePatchAndRun(t *testing.T) {
	handler, queue, clock := newAPI(t)
	runAt := clock.now.Add(72 * time.Hour)
	body := scheduleFollowUpRequest{
		AnchorMessageID: "message-1", AnchorAt: clock.now, RunAt: runAt,
		Content: core.MessageContent{TemplateTag: "remind-employer"},
		Policy:  core.FollowUpPolicy{CancelOnIncoming: true, RequireActiveConversation: true, MaxFollowUps: 1},
	}
	response := performRequest(t, handler, http.MethodPost, "/api/v1/conversations/conversation-1/follow-ups", "follow-up-1", "", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("schedule status: %d body=%s", response.Code, response.Body.String())
	}
	var followUp core.FollowUp
	if err := json.Unmarshal(response.Body.Bytes(), &followUp); err != nil || followUp.Revision != 1 {
		t.Fatalf("schedule response: %#v err=%v", followUp, err)
	}
	patchedRunAt := runAt.Add(time.Hour)
	response = performRequest(t, handler, http.MethodPatch, "/api/v1/follow-ups/"+string(followUp.ID), "", "1", rescheduleFollowUpRequest{RunAt: patchedRunAt})
	if response.Code != http.StatusOK {
		t.Fatalf("patch status: %d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &followUp); err != nil || followUp.Revision != 2 || !followUp.RunAt.Equal(patchedRunAt) {
		t.Fatalf("patch response: %#v err=%v", followUp, err)
	}
	response = performRequest(t, handler, http.MethodPost, "/api/v1/follow-ups/"+string(followUp.ID)+"/run", "run-1", "2", nil)
	if response.Code != http.StatusAccepted || len(queue.Tasks()) != 1 {
		t.Fatalf("run status: %d tasks=%d body=%s", response.Code, len(queue.Tasks()), response.Body.String())
	}
	response = performRequest(t, handler, http.MethodGet, "/api/v1/conversations/conversation-1/follow-ups", "", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status: %d body=%s", response.Code, response.Body.String())
	}
	var list listResponse[core.FollowUp]
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].Revision != 3 {
		t.Fatalf("list response: %#v err=%v", list, err)
	}
}

func TestConversationAPIRejectsUnknownJSONFields(t *testing.T) {
	handler, _, _ := newAPI(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/conversation-1/messages", bytes.NewBufferString(`{"content":{"text":"hello"},"unknown":true}`))
	request.Header.Set("Idempotency-Key", "send-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status: %d body=%s", response.Code, response.Body.String())
	}
}
