package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
)

type ApplicationCampaignPayload struct {
	CampaignID       ApplicationCampaignID `json:"campaign_id,omitempty"`
	ExpectedRevision uint64                `json:"expected_revision,omitempty"`
	JobTag           string                `json:"job_tag,omitempty"`
	Profiles         []ProfileID           `json:"profiles,omitempty"`
	Routes           []SearchID            `json:"routes,omitempty"`
	TargetSuccessful int                   `json:"target_successful,omitempty"`
	MaxInFlight      int                   `json:"max_in_flight,omitempty"`
}

func (payload ApplicationCampaignPayload) Validate() error {
	if payload.CampaignID != "" {
		if payload.ExpectedRevision == 0 {
			return errors.New("application campaign tick requires expected revision")
		}
		if payload.JobTag != "" || len(payload.Profiles) != 0 || len(payload.Routes) != 0 || payload.TargetSuccessful != 0 || payload.MaxInFlight != 0 {
			return errors.New("application campaign tick must not redefine the campaign")
		}
		return nil
	}
	if payload.ExpectedRevision != 0 || strings.TrimSpace(payload.JobTag) == "" ||
		payload.TargetSuccessful < 1 || payload.MaxInFlight < 1 {
		return errors.New("application campaign start requires job, target and max in flight")
	}
	if err := validateCampaignIDs(payload.Profiles, "profile"); err != nil {
		return err
	}
	return validateCampaignIDs(payload.Routes, "route")
}

func NewApplicationCampaignStartPayload(jobTag string, profiles []ProfileID, routes []SearchID, targetSuccessful, maxInFlight int) ApplicationCampaignPayload {
	return ApplicationCampaignPayload{
		JobTag: strings.TrimSpace(jobTag), Profiles: slices.Clone(profiles), Routes: slices.Clone(routes),
		TargetSuccessful: targetSuccessful, MaxInFlight: maxInFlight,
	}
}

func NewApplicationCampaignTickPayload(campaignID ApplicationCampaignID, revision uint64) ApplicationCampaignPayload {
	return ApplicationCampaignPayload{CampaignID: campaignID, ExpectedRevision: revision}
}

type ApplicationSubmitPayload struct {
	ApplicationID ApplicationID  `json:"application_id"`
	Key           ApplicationKey `json:"key"`
}

type ResumePublishPayload struct {
	ProfileID ProfileID `json:"profile_id"`
	ResumeID  string    `json:"resume_id"`
}

type ResumeTouchPayload struct {
	ProfileID ProfileID `json:"profile_id"`
	ResumeID  string    `json:"resume_id"`
}

type ProfileStateApplyPayload struct {
	ProposalID ProfileStateProposalID `json:"proposal_id"`
}

type ProfileStateReconcilePayload struct {
	ResourceTag string `json:"resource_tag"`
}

func (payload ProfileStateReconcilePayload) Validate() error {
	if strings.TrimSpace(payload.ResourceTag) == "" {
		return errors.New("profile state reconcile payload requires resource")
	}
	return nil
}

func ProfileStateReconcileIdempotencyKey(resourceTag, requestKey string) (string, error) {
	resourceTag = strings.TrimSpace(resourceTag)
	requestKey = strings.TrimSpace(requestKey)
	if resourceTag == "" || requestKey == "" {
		return "", errors.New("profile state reconcile idempotency requires resource and request key")
	}
	digest := sha256.Sum256([]byte(resourceTag + "\x00" + requestKey))
	return "profile_state.reconcile:" + hex.EncodeToString(digest[:]), nil
}

func (payload ProfileStateApplyPayload) Validate() error {
	if payload.ProposalID == "" {
		return errors.New("profile state apply payload requires proposal")
	}
	return nil
}

func ProfileStateApplyIdempotencyKey(proposal ProfileStateProposal) (string, error) {
	if err := proposal.Validate(); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(string(proposal.ID) + "\x00" + proposal.DesiredDigest))
	return "profile_state.apply:" + hex.EncodeToString(digest[:]), nil
}

func (payload ResumeTouchPayload) Validate() error {
	if strings.TrimSpace(string(payload.ProfileID)) == "" || strings.TrimSpace(payload.ResumeID) == "" {
		return errors.New("resume touch payload requires profile and resume")
	}
	return nil
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
