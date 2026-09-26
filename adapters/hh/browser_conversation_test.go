package hh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

func TestBrowserConversationDiscoversPaginatedCatalog(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/chatik/api/chats" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Cookie") == "" || request.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing browser request headers: %#v", request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("from") == "" {
			_, _ = writer.Write([]byte(`{
				"chats":{"items":[{"id":41,"currentParticipantId":"me","unreadCount":3,"unknown":"ignored","lastMessage":{
					"id":101,"chatId":41,"creationTime":"2026-09-07T09:00:00Z","text":"","type":"NEGOTIATION_STATE","participantId":"me"
				}}],"nextFrom":77},"resources":{"vacancies":{"42":{"name":"ignored"}}}
			}`))
			return
		}
		if request.URL.Query().Get("from") != "77" {
			t.Errorf("from = %q", request.URL.Query().Get("from"))
		}
		_, _ = writer.Write([]byte(`{"chats":{"items":[{"id":"42","currentParticipantId":"me","lastMessage":{
			"id":"102","chatId":"42","creationTime":"2026-09-07T10:00:00Z","text":"Здравствуйте","type":"SIMPLE","participantId":"employer"
		}}],"nextFrom":null}}`))
	}))
	defer server.Close()
	client := newTestBrowserConversationClient(t, server, adapter.BrowserConversationOptions{})

	result, err := client.DiscoverConversations(context.Background(), "primary")
	if err != nil {
		t.Fatalf("discover conversations: %v", err)
	}
	if requests.Load() != 2 || len(result.Conversations) != 2 || result.ObservedAt.IsZero() {
		t.Fatalf("result=%#v requests=%d", result, requests.Load())
	}
	first := result.Conversations[0]
	if first.ExternalID != "41" || first.UnreadCount != 3 || first.LastMessage == nil || first.LastMessage.Kind != core.MessageSystem || first.LastMessage.Direction != core.MessageOutgoing {
		t.Fatalf("first conversation = %#v", first)
	}
	second := result.Conversations[1]
	if second.LastMessage == nil || second.LastMessage.Text != "Здравствуйте" || second.LastMessage.Direction != core.MessageIncoming {
		t.Fatalf("second conversation = %#v", second)
	}
}

func TestBrowserConversationSyncsAllMessagePagesInChronologicalOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("lastMessageId") == "" {
			_, _ = writer.Write([]byte(`{"chat":{"id":41,"currentParticipantId":"me","resources":{"VACANCY":[42]},"messages":{"items":[
				{"id":2,"chatId":41,"creationTime":"2026-09-07T10:00:00Z","text":"Ответ","type":"SIMPLE","participantId":"employer"},
				{"id":3,"chatId":41,"creationTime":"2026-09-07T11:00:00Z","text":"Спасибо","type":"SIMPLE","participantId":"me"}
			],"hasMore":true}},"chatStates":{"writeMessageState":{"allowed":true}},"display":{"title":"Go developer","subtitle":"Example fallback"},"resources":{"vacancies":{"42":{"name":"Senior Go developer","company":{"visibleName":"Example"},"links":{"desktop":"https://hh.ru/vacancy/42"}}}}}`))
			return
		}
		if request.URL.Query().Get("lastMessageId") != "2" {
			t.Errorf("lastMessageId = %q", request.URL.Query().Get("lastMessageId"))
		}
		_, _ = writer.Write([]byte(`{"chat":{"id":41,"currentParticipantId":"me","messages":{"items":[
			{"id":1,"chatId":41,"creationTime":"2026-09-07T09:00:00Z","text":"Первое","type":"SIMPLE","participantId":"me"}
		],"hasMore":false}}}`))
	}))
	defer server.Close()
	client := newTestBrowserConversationClient(t, server, adapter.BrowserConversationOptions{})

	result, err := client.SyncConversation(context.Background(), "primary", "conversation-1", "41")
	if err != nil {
		t.Fatalf("sync conversation: %v", err)
	}
	if len(result.Messages) != 3 || result.Messages[0].ExternalID != "1" || result.Messages[2].ExternalID != "3" {
		t.Fatalf("messages = %#v", result.Messages)
	}
	if result.Messages[1].Direction != core.MessageIncoming || result.Messages[2].Status != core.MessageSent {
		t.Fatalf("normalized messages = %#v", result.Messages)
	}
	if result.Presentation.VacancyTitle != "Senior Go developer" || result.Presentation.Employer != "Example" || result.Presentation.VacancyURL != "https://hh.ru/vacancy/42" {
		t.Fatalf("presentation = %#v", result.Presentation)
	}
}

func TestBrowserConversationSendsOnlyWithExplicitPermissionAndStableKey(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(`{"chat":{"id":41,"currentParticipantId":"me","messages":{"items":[],"hasMore":false}},"chatStates":{"writeMessageState":{"allowed":true}}}`))
			return
		}
		posts.Add(1)
		if request.URL.Path != "/chatik/api/send" || request.Header.Get("X-Xsrftoken") != "xsrf-value" {
			t.Errorf("send request = %s headers=%#v", request.URL.String(), request.Header)
		}
		var payload struct {
			ChatID         int64  `json:"chatId"`
			IdempotencyKey string `json:"idempotencyKey"`
			Text           string `json:"text"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.ChatID != 41 || payload.Text != "Добрый день" || payload.IdempotencyKey != chatIdempotencyUUID("send-request-1") {
			t.Errorf("payload = %#v", payload)
		}
		_, _ = writer.Write([]byte(`{"id":103,"chatId":41,"creationTime":"2026-09-07T12:00:00Z","text":"Добрый день","type":"SIMPLE","participantId":"me"}`))
	}))
	defer server.Close()
	command := adapter.ConversationSendCommand{
		ProfileID: "primary", ConversationID: "conversation-1", ExternalConversationID: "41",
		Text: "Добрый день", IdempotencyKey: "send-request-1",
	}
	blocked := newTestBrowserConversationClient(t, server, adapter.BrowserConversationOptions{})
	if _, err := blocked.SendConversationMessage(context.Background(), command); !core.ErrorIsCategory(err, core.ErrorUnsupported) {
		t.Fatalf("blocked send error = %v", err)
	}
	if posts.Load() != 0 {
		t.Fatalf("blocked client sent %d POST requests", posts.Load())
	}

	client := newTestBrowserConversationClient(t, server, adapter.BrowserConversationOptions{AllowSend: true})
	message, err := client.SendConversationMessage(context.Background(), command)
	if err != nil {
		t.Fatalf("send conversation message: %v", err)
	}
	if posts.Load() != 1 || message.ExternalID != "103" || message.Direction != core.MessageOutgoing || message.Status != core.MessageSent {
		t.Fatalf("message=%#v posts=%d", message, posts.Load())
	}
}

func TestBrowserConversationMarksLatestIncomingMessageRead(t *testing.T) {
	var marked int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(`{"chat":{"id":41,"currentParticipantId":"me","unreadCount":2,"messages":{"items":[
				{"id":10,"chatId":41,"creationTime":"2026-09-07T10:00:00Z","text":"Первое","type":"SIMPLE","participantId":"employer"},
				{"id":11,"chatId":41,"creationTime":"2026-09-07T10:01:00Z","text":"Моё","type":"SIMPLE","participantId":"me"},
				{"id":12,"chatId":41,"creationTime":"2026-09-07T10:02:00Z","text":"Второе","type":"SIMPLE","participantId":"employer"}
			],"hasMore":false}}}`))
			return
		}
		var payload struct {
			MessageID int64 `json:"messageId"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode mark-read: %v", err)
		}
		marked = payload.MessageID
		_, _ = writer.Write([]byte(`{"ok":true,"extra":"ignored"}`))
	}))
	defer server.Close()
	client := newTestBrowserConversationClient(t, server, adapter.BrowserConversationOptions{AllowMarkRead: true})

	if err := client.MarkConversationRead(context.Background(), "primary", "41"); err != nil {
		t.Fatalf("mark conversation read: %v", err)
	}
	if marked != 12 {
		t.Fatalf("marked message = %d, want 12", marked)
	}
}

func newTestBrowserConversationClient(t *testing.T, server *httptest.Server, options adapter.BrowserConversationOptions) *BrowserConversationClient {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	state := browserStorageState{Cookies: []browserCookie{
		{Name: "session", Value: "opaque", Domain: parsed.Hostname(), Path: "/"},
		{Name: "_xsrf", Value: "xsrf-value", Domain: parsed.Hostname(), Path: "/"},
	}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(stateFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewBrowserConversationClient("primary", stateFile, "JobAgent/Test", server.Client(), options)
	if err != nil {
		t.Fatal(err)
	}
	client.reader.webBaseURL = server.URL
	client.chatBaseURL = server.URL
	return client
}

func TestBrowserConversationDiscoveryTruncatesAtRecentWindow(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sequence := requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"chats":{"items":[{"id":%d,"currentParticipantId":"me"}],"nextFrom":%d}}`, sequence, sequence+1000)
	}))
	defer server.Close()
	client := newTestBrowserConversationClient(t, server, adapter.BrowserConversationOptions{})

	result, err := client.DiscoverConversations(context.Background(), "primary")
	if err != nil {
		t.Fatalf("discover conversations: %v", err)
	}
	if !result.Truncated || len(result.Conversations) != maxChatDiscoveryPages || requests.Load() != int32(maxChatDiscoveryPages) {
		t.Fatalf("result=%#v requests=%d", result, requests.Load())
	}
}

func TestBrowserConversationTreatsDuplicateSendAsDelivered(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(`{"chat":{"id":41,"currentParticipantId":"me","messages":{"items":[
				{"id":10,"chatId":41,"creationTime":"2026-09-07T10:00:00Z","text":"Да","type":"SIMPLE","participantId":"me"}
			],"hasMore":false}},"chatStates":{"writeMessageState":{"allowed":true}}}`))
			return
		}
		writer.WriteHeader(http.StatusConflict)
		_, _ = writer.Write([]byte(`{"error":"duplicate"}`))
	}))
	defer server.Close()
	client := newTestBrowserConversationClient(t, server, adapter.BrowserConversationOptions{AllowSend: true})
	message, err := client.SendConversationMessage(context.Background(), adapter.ConversationSendCommand{
		ProfileID: "primary", ConversationID: "conversation-1", ExternalConversationID: "41",
		Text: "Да", IdempotencyKey: "answer-1",
	})
	if err != nil {
		t.Fatalf("duplicate send: %v", err)
	}
	if message.ExternalID != "10" || message.Direction != core.MessageOutgoing || message.Status != core.MessageSent {
		t.Fatalf("message=%#v", message)
	}
}

func TestBrowserConversationTreatsInvitationPromptAsSuggestion(t *testing.T) {
	var raw hhChatMessage
	if err := json.Unmarshal([]byte(`{"id":12,"creationTime":"2026-09-25T08:40:11Z","text":"Ответьте на приглашение, даже если оно вам не интересно. Так мы сможем рекомендовать вам более подходящие вакансии. Отправить ответ можно одной кнопкой:","type":"SIMPLE","participantId":"employer","actions":{"text_buttons":[{"text":"Посмотрю вакансию, спасибо"},{"text":"Интересно, обсудим детали?"},{"text":"К сожалению, не подходит"}]}}`), &raw); err != nil {
		t.Fatalf("decode invitation fixture: %v", err)
	}
	observation, include, err := mapHHMessageObservation(raw, "me")
	if err != nil || !include {
		t.Fatalf("observation include=%v err=%v", include, err)
	}
	if observation.Kind != core.MessageSuggestion || len(observation.Options) != 3 {
		t.Fatalf("kind=%s options=%d", observation.Kind, len(observation.Options))
	}

	var questionnaire hhChatMessage
	if err := json.Unmarshal([]byte(`{"id":13,"creationTime":"2026-09-25T08:41:11Z","text":"Готовы ли вы работать в офисе?","type":"SIMPLE","participantId":"employer","actions":{"text_buttons":[{"text":"Да"},{"text":"Нет"}]}}`), &questionnaire); err != nil {
		t.Fatalf("decode questionnaire fixture: %v", err)
	}
	observation, include, err = mapHHMessageObservation(questionnaire, "me")
	if err != nil || !include || observation.Kind != core.MessageQuestionnaire {
		t.Fatalf("questionnaire kind=%s include=%v err=%v", observation.Kind, include, err)
	}
}
