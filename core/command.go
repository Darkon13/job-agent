package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

type ApplicationSubmitPayload struct {
	ApplicationID ApplicationID  `json:"application_id"`
	Key           ApplicationKey `json:"key"`
}

type ResumePublishPayload struct {
	ProfileID ProfileID `json:"profile_id"`
	ResumeID  string    `json:"resume_id"`
}

func (payload ResumePublishPayload) Validate() error {
	if strings.TrimSpace(string(payload.ProfileID)) == "" || strings.TrimSpace(payload.ResumeID) == "" {
		return errors.New("resume publish payload requires profile and resume")
	}
	return nil
}

func (payload ApplicationSubmitPayload) Validate() error {
	if payload.ApplicationID == "" {
		return errors.New("application submit payload requires application id")
	}
	return payload.Key.Validate()
}

// ApplicationSubmitIdempotencyKey is stable across retries and process restarts.
// Hashing avoids delimiter ambiguity in external platform IDs.
func ApplicationSubmitIdempotencyKey(key ApplicationKey) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(string(key.ProfileID) + "\x00" + string(key.Vacancy.Platform) + "\x00" + key.Vacancy.ExternalID))
	return "application.submit:" + hex.EncodeToString(digest[:]), nil
}

type ConversationSendPayload struct {
	ConversationID ConversationID `json:"conversation_id"`
	ReplyToID      MessageID      `json:"reply_to_id,omitempty"`
	Content        MessageContent `json:"content"`
}

func (payload ConversationSendPayload) Validate() error {
	if payload.ConversationID == "" {
		return errors.New("conversation send payload requires conversation id")
	}
	return payload.Content.Validate()
}

type ConversationFollowUpPayload struct {
	FollowUpID FollowUpID `json:"follow_up_id"`
}

func (payload ConversationFollowUpPayload) Validate() error {
	if payload.FollowUpID == "" {
		return errors.New("conversation follow-up payload requires follow-up id")
	}
	return nil
}

// ConversationSendIdempotencyKey scopes a client-provided request key to one
// conversation and hides arbitrary external key contents from broker indexes.
func ConversationSendIdempotencyKey(conversationID ConversationID, requestKey string) (string, error) {
	if conversationID == "" || strings.TrimSpace(requestKey) == "" {
		return "", errors.New("conversation send idempotency requires conversation and request key")
	}
	digest := sha256.Sum256([]byte(string(conversationID) + "\x00" + requestKey))
	return "conversation.send:" + hex.EncodeToString(digest[:]), nil
}

func ConversationFollowUpIdempotencyKey(followUpID FollowUpID) (string, error) {
	if followUpID == "" {
		return "", errors.New("conversation follow-up idempotency requires follow-up id")
	}
	digest := sha256.Sum256([]byte(followUpID))
	return "conversation.follow_up:" + hex.EncodeToString(digest[:]), nil
}

func ConversationFollowUpRequestIdempotencyKey(conversationID ConversationID, requestKey string) (string, error) {
	if conversationID == "" || strings.TrimSpace(requestKey) == "" {
		return "", errors.New("follow-up request idempotency requires conversation and request key")
	}
	digest := sha256.Sum256([]byte(string(conversationID) + "\x00" + requestKey))
	return "conversation.follow_up.request:" + hex.EncodeToString(digest[:]), nil
}

type ConversationIDPayload struct {
	ConversationID ConversationID `json:"conversation_id"`
}

func (payload ConversationIDPayload) Validate() error {
	if payload.ConversationID == "" {
		return errors.New("conversation command payload requires conversation id")
	}
	return nil
}
