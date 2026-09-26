package hh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

const (
	defaultChatBaseURL    = "https://chatik.hh.ru"
	maxChatResponse       = 16 << 20
	maxChatPages          = 100
	maxChatDiscoveryPages = 50
	hhSystemMessagePrefix = "Событие переговоров HH"
)

// BrowserConversationClient talks to HH's chatik endpoints using an exported
// browser state. Read access is provided by binding the client; every mutating
// capability remains an explicit profile-level permission.
type BrowserConversationClient struct {
	reader      *BrowserReadClient
	options     adapter.BrowserConversationOptions
	chatBaseURL string
	mu          sync.Mutex
}

var _ adapter.ConversationTransport = (*BrowserConversationClient)(nil)
var _ adapter.ConversationDiscoverer = (*BrowserConversationClient)(nil)

type hhChatListResponse struct {
	Chats struct {
		Items    []hhChatListItem `json:"items"`
		NextFrom flexibleID       `json:"nextFrom"`
	} `json:"chats"`
}

type hhChatListItem struct {
	ID                   flexibleID       `json:"id"`
	CurrentParticipantID string           `json:"currentParticipantId"`
	LastMessage          *hhChatMessage   `json:"lastMessage"`
	LastActivityTime     string           `json:"lastActivityTime"`
	Type                 string           `json:"type"`
	SubType              string           `json:"subType"`
	UnreadCount          int              `json:"unreadCount"`
	Operations           hhChatOperations `json:"operations"`
}

type hhChatOperations struct {
	Allowed []string `json:"allowed"`
}

type hhChatDataResponse struct {
	Chat struct {
		ID                   flexibleID         `json:"id"`
		CurrentParticipantID string             `json:"currentParticipantId"`
		UnreadCount          int                `json:"unreadCount"`
		Messages             hhChatMessages     `json:"messages"`
		WritePossibility     hhWritePossibility `json:"writePossibility"`
		Resources            struct {
			Vacancies flexibleIDs `json:"VACANCY"`
		} `json:"resources"`
	} `json:"chat"`
	ChatStates struct {
		WriteMessageState hhWriteMessageState `json:"writeMessageState"`
	} `json:"chatStates"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error,omitempty"`
	Display struct {
		Title    string `json:"title"`
		Subtitle string `json:"subtitle"`
	} `json:"display"`
	Resources struct {
		Vacancies map[string]struct {
			Name    string `json:"name"`
			Company struct {
				Name        string `json:"name"`
				VisibleName string `json:"visibleName"`
			} `json:"company"`
			Links struct {
				Desktop string `json:"desktop"`
			} `json:"links"`
		} `json:"vacancies"`
	} `json:"resources"`
}

type hhChatMessages struct {
	Items   []hhChatMessage `json:"items"`
	HasMore bool            `json:"hasMore"`
}

type hhWritePossibility struct {
	Name                 string   `json:"name"`
	WriteDisabledReasons []string `json:"writeDisabledReasons"`
}

type hhWriteMessageState struct {
	Allowed bool     `json:"allowed"`
	Reason  string   `json:"reason"`
	Reasons []string `json:"reasons"`
}

type hhChatMessage struct {
	ID            flexibleID `json:"id"`
	ChatID        flexibleID `json:"chatId"`
	CreationTime  string     `json:"creationTime"`
	Text          string     `json:"text"`
	Type          string     `json:"type"`
	ParticipantID string     `json:"participantId"`
	Hidden        bool       `json:"hidden"`
	Deleted       bool       `json:"deleted"`
	Actions       struct {
		TextButtons []struct {
			Text string `json:"text"`
		} `json:"text_buttons"`
	} `json:"actions"`
}

func NewBrowserConversationClient(profileID core.ProfileID, stateFile, userAgent string, httpClient *http.Client, options adapter.BrowserConversationOptions) (*BrowserConversationClient, error) {
	reader, err := NewBrowserReadClient(profileID, stateFile, userAgent, httpClient)
	if err != nil {
		return nil, err
	}
	return newBrowserConversationClient(reader, options), nil
}

func newBrowserConversationClient(reader *BrowserReadClient, options adapter.BrowserConversationOptions) *BrowserConversationClient {
	return &BrowserConversationClient{reader: reader, options: options, chatBaseURL: defaultChatBaseURL}
}

func (client *BrowserConversationClient) DiscoverConversations(ctx context.Context, profileID core.ProfileID) (adapter.ConversationDiscoveryResult, error) {
	if profileID == "" || profileID != client.reader.profileID {
		return adapter.ConversationDiscoveryResult{}, errors.New("HH conversation discovery profile does not match")
	}
	client.mu.Lock()
	defer client.mu.Unlock()

	observedAt := time.Now().UTC()
	result := make([]core.ConversationObservation, 0)
	seenConversations := make(map[string]struct{})
	truncated := true
	nextFrom := ""
	for page := 0; page < maxChatDiscoveryPages; page++ {
		parameters := url.Values{
			"filterUnread":         {"false"},
			"filterHasTextMessage": {"false"},
		}
		if nextFrom != "" {
			parameters.Set("from", nextFrom)
		}
		var response hhChatListResponse
		if err := client.getJSON(ctx, "/chatik/api/chats", parameters, &response, "conversations.discover.browser"); err != nil {
			return adapter.ConversationDiscoveryResult{}, err
		}
		for _, item := range response.Chats.Items {
			externalID := strings.TrimSpace(string(item.ID))
			if externalID == "" {
				return adapter.ConversationDiscoveryResult{}, operationError(core.ErrorPermanentFailure, "conversations.discover.browser", "HH returned a conversation without id", nil)
			}
			if _, exists := seenConversations[externalID]; exists {
				continue
			}
			seenConversations[externalID] = struct{}{}
			observation := core.ConversationObservation{
				ExternalID: externalID, Status: core.ConversationActive, UnreadCount: item.UnreadCount,
			}
			if item.LastMessage != nil {
				message, include, err := mapHHMessageObservation(*item.LastMessage, item.CurrentParticipantID)
				if err != nil {
					return adapter.ConversationDiscoveryResult{}, err
				}
				if include {
					observation.LastMessage = &message
				}
			}
			result = append(result, observation)
		}
		candidate := strings.TrimSpace(string(response.Chats.NextFrom))
		if candidate == "" {
			truncated = false
			break
		}
		if candidate == nextFrom {
			return adapter.ConversationDiscoveryResult{}, operationError(core.ErrorTemporaryFailure, "conversations.discover.browser", "HH conversation cursor did not advance", nil)
		}
		nextFrom = candidate
	}
	if truncated {
		// The account can have thousands of historical chats; the catalog is
		// ordered by last activity, so a bounded recent window keeps the active
		// set fresh. Chats with a stale unread badge fall outside that window,
		// so the unread filter fetches them explicitly and the platform badge
		// stays clearable from the local catalog.
		if err := client.appendUnreadConversations(ctx, seenConversations, &result); err != nil {
			return adapter.ConversationDiscoveryResult{}, err
		}
	}
	return adapter.ConversationDiscoveryResult{Conversations: result, ObservedAt: observedAt, Truncated: truncated}, nil
}

// appendUnreadConversations adds chats beyond the recent-activity window that
// still carry unread messages. The unread filter returns a bounded list, so the
// pass stays cheap even when the full catalog is large.
func (client *BrowserConversationClient) appendUnreadConversations(ctx context.Context, seen map[string]struct{}, result *[]core.ConversationObservation) error {
	const operation = "conversations.discover.unread.browser"
	nextFrom := ""
	for page := 0; page < maxChatDiscoveryPages; page++ {
		parameters := url.Values{
			"filterUnread":         {"true"},
			"filterHasTextMessage": {"false"},
		}
		if nextFrom != "" {
			parameters.Set("from", nextFrom)
		}
		var response hhChatListResponse
		if err := client.getJSON(ctx, "/chatik/api/chats", parameters, &response, operation); err != nil {
			return err
		}
		for _, item := range response.Chats.Items {
			externalID := strings.TrimSpace(string(item.ID))
			if externalID == "" {
				return operationError(core.ErrorPermanentFailure, operation, "HH returned a conversation without id", nil)
			}
			if _, exists := seen[externalID]; exists {
				continue
			}
			seen[externalID] = struct{}{}
			observation := core.ConversationObservation{
				ExternalID: externalID, Status: core.ConversationActive, UnreadCount: item.UnreadCount,
			}
			if item.LastMessage != nil {
				message, include, err := mapHHMessageObservation(*item.LastMessage, item.CurrentParticipantID)
				if err != nil {
					return err
				}
				if include {
					observation.LastMessage = &message
				}
			}
			*result = append(*result, observation)
		}
		candidate := strings.TrimSpace(string(response.Chats.NextFrom))
		if candidate == "" {
			return nil
		}
		if candidate == nextFrom {
			return operationError(core.ErrorTemporaryFailure, operation, "HH conversation cursor did not advance", nil)
		}
		nextFrom = candidate
	}
	return nil
}

func (client *BrowserConversationClient) SyncConversation(ctx context.Context, profileID core.ProfileID, conversationID core.ConversationID, externalConversationID string) (adapter.ConversationSyncResult, error) {
	if err := client.validateIdentity(profileID, conversationID, externalConversationID); err != nil {
		return adapter.ConversationSyncResult{}, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()

	chatID, err := parseHHNumericID(externalConversationID, "conversation")
	if err != nil {
		return adapter.ConversationSyncResult{}, err
	}
	observedAt := time.Now().UTC()
	messages := make([]core.ConversationMessage, 0)
	seen := make(map[string]struct{})
	presentation := core.ConversationPresentation{}
	lastMessageID := ""
	for page := 0; page < maxChatPages; page++ {
		data, err := client.fetchChatData(ctx, chatID, lastMessageID, "conversations.sync.browser")
		if err != nil {
			return adapter.ConversationSyncResult{}, err
		}
		if string(data.Chat.ID) != externalConversationID {
			return adapter.ConversationSyncResult{}, operationError(core.ErrorPermanentFailure, "conversations.sync.browser", "HH returned another conversation", nil)
		}
		if page == 0 {
			presentation = mapHHConversationPresentation(data)
		}
		for _, raw := range data.Chat.Messages.Items {
			observation, include, err := mapHHMessageObservation(raw, data.Chat.CurrentParticipantID)
			if err != nil {
				return adapter.ConversationSyncResult{}, err
			}
			if !include {
				continue
			}
			if _, exists := seen[observation.ExternalID]; exists {
				continue
			}
			seen[observation.ExternalID] = struct{}{}
			message, err := observation.Message(hhMessageID(observation.ExternalID), conversationID)
			if err != nil {
				return adapter.ConversationSyncResult{}, err
			}
			messages = append(messages, message)
		}
		if !data.Chat.Messages.HasMore || len(data.Chat.Messages.Items) == 0 {
			sortConversationMessages(messages)
			return adapter.ConversationSyncResult{Messages: messages, Presentation: presentation, ObservedAt: observedAt}, nil
		}
		candidate := string(data.Chat.Messages.Items[0].ID)
		if candidate == "" || candidate == lastMessageID {
			return adapter.ConversationSyncResult{}, operationError(core.ErrorTemporaryFailure, "conversations.sync.browser", "HH message cursor did not advance", nil)
		}
		lastMessageID = candidate
	}
	return adapter.ConversationSyncResult{}, operationError(core.ErrorTemporaryFailure, "conversations.sync.browser", "HH message pagination exceeded its safety limit", nil)
}

func mapHHConversationPresentation(data hhChatDataResponse) core.ConversationPresentation {
	presentation := core.ConversationPresentation{
		VacancyTitle: strings.TrimSpace(data.Display.Title),
		Employer:     strings.TrimSpace(data.Display.Subtitle),
	}
	ids := append([]string(nil), data.Chat.Resources.Vacancies...)
	if len(ids) == 0 {
		for id := range data.Resources.Vacancies {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	for _, id := range ids {
		vacancy, exists := data.Resources.Vacancies[id]
		if !exists {
			continue
		}
		if title := strings.TrimSpace(vacancy.Name); title != "" {
			presentation.VacancyTitle = title
		}
		employer := strings.TrimSpace(vacancy.Company.VisibleName)
		if employer == "" {
			employer = strings.TrimSpace(vacancy.Company.Name)
		}
		if employer != "" {
			presentation.Employer = employer
		}
		presentation.VacancyURL = strings.TrimSpace(vacancy.Links.Desktop)
		break
	}
	return presentation
}

func (client *BrowserConversationClient) SendConversationMessage(ctx context.Context, command adapter.ConversationSendCommand) (core.ConversationMessage, error) {
	if err := client.validateIdentity(command.ProfileID, command.ConversationID, command.ExternalConversationID); err != nil {
		return core.ConversationMessage{}, err
	}
	if strings.TrimSpace(command.Text) == "" || strings.TrimSpace(command.IdempotencyKey) == "" {
		return core.ConversationMessage{}, errors.New("HH conversation send requires text and idempotency key")
	}
	if !client.options.AllowSend {
		return core.ConversationMessage{}, hhUnsupported("conversations.send.browser")
	}
	client.mu.Lock()
	defer client.mu.Unlock()

	chatID, err := parseHHNumericID(command.ExternalConversationID, "conversation")
	if err != nil {
		return core.ConversationMessage{}, err
	}
	data, err := client.fetchChatData(ctx, chatID, "", "conversations.send.preflight.browser")
	if err != nil {
		return core.ConversationMessage{}, err
	}
	if !data.ChatStates.WriteMessageState.Allowed {
		reason := data.ChatStates.WriteMessageState.Reason
		if reason == "" && len(data.ChatStates.WriteMessageState.Reasons) != 0 {
			reason = strings.Join(data.ChatStates.WriteMessageState.Reasons, ",")
		}
		category := core.ErrorPermanentFailure
		if reason == "TEMPORARILY_UNAVAILABLE" || reason == "AI_ASSISTANT_TYPING" {
			category = core.ErrorTemporaryFailure
		}
		return core.ConversationMessage{}, &core.OperationError{
			Category: category, Operation: "conversations.send.browser", Platform: Name,
			Message: "HH conversation is not writable", Metadata: map[string]string{"reason": reason},
		}
	}
	payload := struct {
		ChatID         int64  `json:"chatId"`
		IdempotencyKey string `json:"idempotencyKey"`
		Text           string `json:"text"`
	}{ChatID: chatID, IdempotencyKey: chatIdempotencyUUID(command.IdempotencyKey), Text: command.Text}
	var response hhChatMessage
	parameters := url.Values{"hhtmSourceLabel": {"chat"}, "hhtmSource": {"chat"}}
	if err := client.postJSON(ctx, "/chatik/api/send", parameters, payload, &response, "conversations.send.browser", true, command.ExternalConversationID); err != nil {
		if chatConflict(err) {
			// A repeated operator action: the answer is already in the chat.
			return client.resyncSentMessage(ctx, chatID, command)
		}
		return core.ConversationMessage{}, err
	}
	observation, include, err := mapHHMessageObservation(response, data.Chat.CurrentParticipantID)
	if err != nil {
		return core.ConversationMessage{}, err
	}
	if !include {
		return core.ConversationMessage{}, operationError(core.ErrorAmbiguousResult, "conversations.send.browser", "HH accepted the message but returned no usable message", nil)
	}
	observation.Direction = core.MessageOutgoing
	if strings.TrimSpace(observation.Text) == "" {
		observation.Text = command.Text
	}
	message, err := observation.Message(hhMessageID(observation.ExternalID), command.ConversationID)
	if err != nil {
		return core.ConversationMessage{}, err
	}
	message.ReplyToID = command.ReplyToID
	return message, message.Validate()
}

// resyncSentMessage finds the already delivered message for one send command.
func (client *BrowserConversationClient) resyncSentMessage(ctx context.Context, chatID int64, command adapter.ConversationSendCommand) (core.ConversationMessage, error) {
	data, err := client.fetchChatData(ctx, chatID, "", "conversations.send.reconcile.browser")
	if err != nil {
		return core.ConversationMessage{}, err
	}
	for index := len(data.Chat.Messages.Items) - 1; index >= 0; index-- {
		observation, include, err := mapHHMessageObservation(data.Chat.Messages.Items[index], data.Chat.CurrentParticipantID)
		if err != nil || !include {
			continue
		}
		if observation.Direction != core.MessageOutgoing || strings.TrimSpace(observation.Text) != command.Text {
			continue
		}
		message, err := observation.Message(hhMessageID(observation.ExternalID), command.ConversationID)
		if err != nil {
			continue
		}
		return message, message.Validate()
	}
	return core.ConversationMessage{}, operationError(core.ErrorTemporaryFailure, "conversations.send.browser",
		"HH reported a duplicate message but it was not found in the chat", nil)
}

func (client *BrowserConversationClient) MarkConversationRead(ctx context.Context, profileID core.ProfileID, externalConversationID string) error {
	if profileID == "" || profileID != client.reader.profileID || strings.TrimSpace(externalConversationID) == "" {
		return errors.New("HH mark-read conversation identity does not match")
	}
	if !client.options.AllowMarkRead {
		return hhUnsupported("conversations.mark_read.browser")
	}
	client.mu.Lock()
	defer client.mu.Unlock()

	chatID, err := parseHHNumericID(externalConversationID, "conversation")
	if err != nil {
		return err
	}
	data, err := client.fetchChatData(ctx, chatID, "", "conversations.mark_read.preflight.browser")
	if err != nil {
		return err
	}
	if data.Chat.UnreadCount == 0 {
		return nil
	}
	var messageID int64
	hasDiscard := false
	for index := len(data.Chat.Messages.Items) - 1; index >= 0; index-- {
		message := data.Chat.Messages.Items[index]
		if message.ParticipantID == data.Chat.CurrentParticipantID || message.Hidden || message.Deleted {
			continue
		}
		messageID, err = parseHHNumericID(string(message.ID), "message")
		if err != nil {
			return err
		}
		// System/discard events keep their own unread flag on HH; the payload
		// must tell the platform that such a message is included.
		hasDiscard = strings.Contains(strings.ToUpper(message.Type), "DISCARD") || strings.Contains(strings.ToUpper(message.Type), "SYSTEM")
		break
	}
	if messageID == 0 {
		return operationError(core.ErrorTemporaryFailure, "conversations.mark_read.browser", "HH reported unread messages but returned no readable incoming message", nil)
	}
	payload := struct {
		ChatID                  int64 `json:"chatId"`
		MessageID               int64 `json:"messageId"`
		HasUnreadDiscardMessage bool  `json:"hasUnreadDiscardMessage"`
	}{ChatID: chatID, MessageID: messageID, HasUnreadDiscardMessage: hasDiscard}
	return client.postJSON(ctx, "/chatik/api/mark_read", nil, payload, nil, "conversations.mark_read.browser", false, externalConversationID)
}

func (client *BrowserConversationClient) fetchChatData(ctx context.Context, chatID int64, lastMessageID, operation string) (hhChatDataResponse, error) {
	parameters := url.Values{"chatId": {strconv.FormatInt(chatID, 10)}}
	if lastMessageID != "" {
		parameters.Set("lastMessageId", lastMessageID)
	}
	var response hhChatDataResponse
	if err := client.getJSON(ctx, "/chatik/api/chat_data", parameters, &response, operation); err != nil {
		return hhChatDataResponse{}, err
	}
	if response.Error != nil {
		return hhChatDataResponse{}, operationError(core.ErrorPermanentFailure, operation, "HH returned chat error "+response.Error.Code, nil)
	}
	return response, nil
}

func (client *BrowserConversationClient) getJSON(ctx context.Context, requestPath string, parameters url.Values, target any, operation string) error {
	endpoint := strings.TrimRight(client.chatBaseURL, "/") + requestPath
	if len(parameters) != 0 {
		endpoint += "?" + parameters.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	return client.doJSON(request, target, operation, false)
}

func (client *BrowserConversationClient) postJSON(ctx context.Context, requestPath string, parameters url.Values, payload, target any, operation string, ambiguousOnTransport bool, conversationID string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode HH conversation request: %w", err)
	}
	endpoint := strings.TrimRight(client.chatBaseURL, "/") + requestPath
	if len(parameters) != 0 {
		endpoint += "?" + parameters.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", strings.TrimRight(client.reader.webBaseURL, "/"))
	request.Header.Set("Referer", strings.TrimRight(client.reader.webBaseURL, "/")+"/chat/"+url.PathEscape(conversationID))
	return client.doJSON(request, target, operation, ambiguousOnTransport)
}

func (client *BrowserConversationClient) doJSON(request *http.Request, target any, operation string, ambiguousOnTransport bool) error {
	transport, err := NewResumeTouchTransport(client.reader.stateFile, client.reader.httpClient)
	if err != nil {
		return err
	}
	transport.profileURL = request.URL.String()
	transport.touchURL = request.URL.String()
	httpClient, err := transport.authenticatedClient()
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	request.Header.Set("User-Agent", client.reader.userAgent)
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	if xsrf := cookieValue(httpClient, request.URL, "_xsrf"); xsrf != "" {
		request.Header.Set("X-Xsrftoken", xsrf)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		category := core.ErrorTemporaryFailure
		if ambiguousOnTransport {
			category = core.ErrorAmbiguousResult
		}
		return operationError(category, operation, "HH conversation request failed", err)
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxChatResponse))
	if readErr != nil {
		category := core.ErrorTemporaryFailure
		if ambiguousOnTransport {
			category = core.ErrorAmbiguousResult
		}
		return operationError(category, operation, "read HH conversation response", readErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return classifyChatStatus(response, operation)
	}
	if target == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return operationError(core.ErrorTemporaryFailure, operation, "decode HH conversation response", err)
	}
	return nil
}

func classifyChatStatus(response *http.Response, operation string) error {
	category := core.ErrorPermanentFailure
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		category = core.ErrorUnauthorized
	case response.StatusCode == http.StatusTooManyRequests:
		category = core.ErrorRateLimited
	case response.StatusCode >= 500:
		category = core.ErrorTemporaryFailure
	}
	failure := operationError(category, operation, fmt.Sprintf("HH returned status %d", response.StatusCode), nil)
	failure.Metadata = map[string]string{"http_status": strconv.Itoa(response.StatusCode)}
	if category == core.ErrorRateLimited {
		failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), time.Now().UTC())
	}
	return failure
}

// hhInvitationPrompt reports the employer-invitation prompt: options that ask
// whether the applicant is interested in a received invitation.
func hhInvitationPrompt(text string, options []core.MessageOption) bool {
	haystack := strings.ToLower(text)
	for _, option := range options {
		haystack += " " + strings.ToLower(option.Text)
	}
	for _, marker := range []string{
		"ответьте на приглашение",
		"отправить ответ можно одной кнопкой",
		"посмотрю вакансию, спасибо",
	} {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

// chatConflict reports the duplicate-send answer: HH rejects a message that is
// already in the chat, which is a success for an idempotent operator action.
func chatConflict(err error) bool {
	var operationError *core.OperationError
	if !errors.As(err, &operationError) || operationError.Validate() != nil {
		return false
	}
	status, parseErr := strconv.Atoi(operationError.Metadata["http_status"])
	return parseErr == nil && status == http.StatusConflict
}

func (client *BrowserConversationClient) validateIdentity(profileID core.ProfileID, conversationID core.ConversationID, externalConversationID string) error {
	if profileID == "" || profileID != client.reader.profileID || conversationID == "" || strings.TrimSpace(externalConversationID) == "" {
		return errors.New("HH conversation identity does not match")
	}
	return nil
}

func mapHHMessageObservation(raw hhChatMessage, currentParticipantID string) (core.ConversationMessageObservation, bool, error) {
	if raw.Hidden || raw.Deleted {
		return core.ConversationMessageObservation{}, false, nil
	}
	externalID := strings.TrimSpace(string(raw.ID))
	if externalID == "" {
		return core.ConversationMessageObservation{}, false, operationError(core.ErrorPermanentFailure, "conversations.map.browser", "HH returned a message without id", nil)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, raw.CreationTime)
	if err != nil {
		return core.ConversationMessageObservation{}, false, operationError(core.ErrorPermanentFailure, "conversations.map.browser", "HH returned an invalid message time", err)
	}
	direction := core.MessageIncoming
	if raw.ParticipantID != "" && raw.ParticipantID == currentParticipantID {
		direction = core.MessageOutgoing
	}
	kind := core.MessageText
	text := strings.TrimSpace(raw.Text)
	options := make([]core.MessageOption, 0, len(raw.Actions.TextButtons))
	for index, option := range raw.Actions.TextButtons {
		if value := strings.TrimSpace(option.Text); value != "" {
			options = append(options, core.MessageOption{ID: fmt.Sprintf("%s:%d", externalID, index), Text: value})
		}
	}
	if len(options) != 0 {
		kind = core.MessageQuestionnaire
		if hhInvitationPrompt(text, options) {
			// Invitation buttons look like a questionnaire but only answer an
			// invitation; the chat must not be flagged as a running one.
			kind = core.MessageSuggestion
		}
	}
	if text == "" && len(options) == 0 {
		kind = core.MessageSystem
		messageType := strings.TrimSpace(raw.Type)
		if messageType == "" {
			messageType = "UNKNOWN"
		}
		text = hhSystemMessagePrefix + " (" + messageType + ")"
	} else if raw.Type != "" && raw.Type != "SIMPLE" && len(options) == 0 {
		kind = core.MessageSystem
		// Keep the platform event type in the text: the questionnaire badge and
		// the operator both rely on PARTICIPANT_JOINED/PARTICIPANT_LEFT.
		if messageType := strings.TrimSpace(raw.Type); messageType != "" {
			text = hhSystemMessagePrefix + " (" + messageType + "): " + text
		}
	}
	return core.ConversationMessageObservation{
		ExternalID: externalID, Direction: direction, Kind: kind,
		Text: text, Options: options, OccurredAt: occurredAt.UTC(),
	}, true, nil
}

func parseHHNumericID(value, kind string) (int64, error) {
	number, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("HH %s id must be a positive integer", kind)
	}
	return number, nil
}

func hhMessageID(externalID string) core.MessageID {
	digest := sha256.Sum256([]byte("hh\x00conversation-message\x00" + externalID))
	return core.MessageID("message-hh-" + hex.EncodeToString(digest[:16]))
}

func chatIdempotencyUUID(value string) string {
	digest := sha256.Sum256([]byte("hh\x00conversation-send\x00" + value))
	bytes := append([]byte(nil), digest[:16]...)
	bytes[6] = bytes[6]&0x0f | 0x50
	bytes[8] = bytes[8]&0x3f | 0x80
	hexValue := hex.EncodeToString(bytes)
	return hexValue[0:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:32]
}

func sortConversationMessages(messages []core.ConversationMessage) {
	sort.Slice(messages, func(first, second int) bool {
		if !messages[first].OccurredAt.Equal(messages[second].OccurredAt) {
			return messages[first].OccurredAt.Before(messages[second].OccurredAt)
		}
		return messages[first].ExternalID < messages[second].ExternalID
	})
}
