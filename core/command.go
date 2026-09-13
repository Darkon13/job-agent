package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

type ApplicationRemovePayload struct {
	ApplicationID ApplicationID            `json:"application_id"`
	Reason        ApplicationRemovalReason `json:"reason"`
	StaleAfter    Duration                 `json:"stale_after,omitempty"`
}

func (payload ApplicationRemovePayload) Validate() error {
	if payload.ApplicationID == "" {
		return errors.New("application remove payload requires application id")
	}
	if payload.Reason == ApplicationRemovalRetentionStale && payload.StaleAfter.Value() <= 0 {
		return errors.New("stale application removal requires positive stale_after")
	}
	return payload.Reason.Validate()
}

func ApplicationRemoveIdempotencyKey(applicationID ApplicationID, requestKey string) (string, error) {
	requestKey = strings.TrimSpace(requestKey)
	if applicationID == "" || requestKey == "" {
		return "", errors.New("application remove idempotency requires application and request key")
	}
	digest := sha256.Sum256([]byte(string(applicationID) + "\x00" + requestKey))
	return "application.remove:" + hex.EncodeToString(digest[:]), nil
}

type ApplicationRetentionPayload struct {
	ProfileID      ProfileID `json:"profile_id"`
	StaleAfter     Duration  `json:"stale_after"`
	RemoveRejected bool      `json:"remove_rejected"`
}

func (payload ApplicationRetentionPayload) Validate() error {
	if payload.ProfileID == "" {
		return errors.New("application retention requires profile")
	}
	if payload.StaleAfter.Value() <= 0 {
		return errors.New("application retention requires positive stale_after")
	}
	return nil
}

// ApplicationStateSyncPayload refreshes stored platform states for a profile
// without changing applications, budgets or retention decisions.
type ApplicationStateSyncPayload struct {
	ProfileID ProfileID `json:"profile_id"`
}

func (payload ApplicationStateSyncPayload) Validate() error {
	if payload.ProfileID == "" {
		return errors.New("application state sync requires profile")
	}
	return nil
}

type ResumePublishPayload struct {
	ProfileID ProfileID `json:"profile_id"`
	ResumeID  string    `json:"resume_id"`
}

// TestCapturePayload discovers the questionnaire shown by a vacancy response
// popup. Capturing never starts an attempt and never submits answers.
type TestCapturePayload struct {
	ProfileID         ProfileID `json:"profile_id"`
	Platform          Platform  `json:"platform"`
	VacancyExternalID string    `json:"vacancy_external_id"`
}

func (payload TestCapturePayload) Validate() error {
	if payload.ProfileID == "" {
		return errors.New("test capture requires profile")
	}
	if payload.Platform == "" {
		return errors.New("test capture requires platform")
	}
	if strings.TrimSpace(payload.VacancyExternalID) == "" {
		return errors.New("test capture requires vacancy external id")
	}
	return nil
}

func TestCaptureIdempotencyKey(platform Platform, externalID string, profileID ProfileID) (string, error) {
	externalID = strings.TrimSpace(externalID)
	if platform == "" || externalID == "" || profileID == "" {
		return "", errors.New("test capture idempotency requires platform, vacancy and profile")
	}
	digest := sha256.Sum256([]byte(string(platform) + "\x00" + externalID + "\x00" + string(profileID)))
	return "test.capture:" + hex.EncodeToString(digest[:]), nil
}

// QuestionnaireAnswerPayload submits already resolved runtime answers for a
// vacancy questionnaire. Resolution (known answer, model, or human review)
// happens before the task is created.
type QuestionnaireAnswerPayload struct {
	ProfileID          ProfileID        `json:"profile_id"`
	Platform           Platform         `json:"platform"`
	VacancyExternalID  string           `json:"vacancy_external_id"`
	Answers            []ResolvedAnswer `json:"answers"`
	AttemptFingerprint string           `json:"attempt_fingerprint,omitempty"`
}

func (payload QuestionnaireAnswerPayload) Validate() error {
	if payload.ProfileID == "" {
		return errors.New("questionnaire answer requires profile")
	}
	if payload.Platform == "" {
		return errors.New("questionnaire answer requires platform")
	}
	if strings.TrimSpace(payload.VacancyExternalID) == "" {
		return errors.New("questionnaire answer requires vacancy external id")
	}
	if len(payload.Answers) == 0 {
		return errors.New("questionnaire answer requires answers")
	}
	if payload.AttemptFingerprint != "" {
		if err := validateSHA256Fingerprint(payload.AttemptFingerprint); err != nil {
			return fmt.Errorf("questionnaire answer attempt fingerprint: %w", err)
		}
	}
	seen := make(map[string]struct{}, len(payload.Answers))
	for _, answer := range payload.Answers {
		if err := answer.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[answer.QuestionID]; duplicate {
			return fmt.Errorf("questionnaire answer repeats question %q", answer.QuestionID)
		}
		seen[answer.QuestionID] = struct{}{}
	}
	return nil
}

func QuestionnaireAnswerIdempotencyKey(platform Platform, externalID string, profileID ProfileID, requestKey string) (string, error) {
	externalID = strings.TrimSpace(externalID)
	requestKey = strings.TrimSpace(requestKey)
	if platform == "" || externalID == "" || profileID == "" || requestKey == "" {
		return "", errors.New("questionnaire answer idempotency requires platform, vacancy, profile and request key")
	}
	digest := sha256.Sum256([]byte(string(platform) + "\x00" + externalID + "\x00" + string(profileID) + "\x00" + requestKey))
	return "questionnaire.answer:" + hex.EncodeToString(digest[:]), nil
}

// TestCompletePayload records the platform outcome of a finished vacancy test.
// It never re-submits answers.
type TestCompletePayload struct {
	ProfileID          ProfileID         `json:"profile_id"`
	Platform           Platform          `json:"platform"`
	VacancyExternalID  string            `json:"vacancy_external_id"`
	Status             TestAttemptStatus `json:"status"`
	AttemptFingerprint string            `json:"attempt_fingerprint,omitempty"`
}

func (payload TestCompletePayload) Validate() error {
	if payload.ProfileID == "" {
		return errors.New("test completion requires profile")
	}
	if payload.Platform == "" {
		return errors.New("test completion requires platform")
	}
	if strings.TrimSpace(payload.VacancyExternalID) == "" {
		return errors.New("test completion requires vacancy external id")
	}
	if payload.Status != TestAttemptSubmitted && payload.Status != TestAttemptPassed && payload.Status != TestAttemptFailed {
		return fmt.Errorf("test completion has unsupported status %q", payload.Status)
	}
	return nil
}

func TestCompleteIdempotencyKey(platform Platform, externalID string, profileID ProfileID, requestKey string) (string, error) {
	externalID = strings.TrimSpace(externalID)
	requestKey = strings.TrimSpace(requestKey)
	if platform == "" || externalID == "" || profileID == "" || requestKey == "" {
		return "", errors.New("test completion idempotency requires platform, vacancy, profile and request key")
	}
	digest := sha256.Sum256([]byte(string(platform) + "\x00" + externalID + "\x00" + string(profileID) + "\x00" + requestKey))
	return "test.complete:" + hex.EncodeToString(digest[:]), nil
}

// ReviewAnswerPayload carries one human selection for the current review
// prompt. It is append-only and uses optimistic concurrency, so a stale client
// never overwrites a newer answer.
// ReviewAnswerEntry is one human answer inside a batch review submission.
type ReviewAnswerEntry struct {
	QuestionID      string   `json:"question_id"`
	SelectedOptions []string `json:"selected_options,omitempty"`
	Text            string   `json:"text,omitempty"`
}

type ReviewAnswerPayload struct {
	SessionID        ReviewSessionID     `json:"session_id"`
	PromptID         ReviewPromptID      `json:"prompt_id,omitempty"`
	ExpectedRevision uint64              `json:"expected_revision"`
	SelectedOptions  []string            `json:"selected_options,omitempty"`
	Text             string              `json:"text,omitempty"`
	Source           string              `json:"source"`
	Answers          []ReviewAnswerEntry `json:"answers,omitempty"`
}

func (payload ReviewAnswerPayload) Validate() error {
	if payload.SessionID == "" {
		return errors.New("review answer requires session")
	}
	if payload.ExpectedRevision == 0 {
		return errors.New("review answer requires expected revision")
	}
	if strings.TrimSpace(payload.Source) == "" {
		return errors.New("review answer requires source")
	}
	if len(payload.Answers) != 0 {
		if payload.PromptID != "" || payload.Text != "" || len(payload.SelectedOptions) != 0 {
			return errors.New("batch review answers must not mix a single prompt answer")
		}
		seen := make(map[string]struct{}, len(payload.Answers))
		for _, entry := range payload.Answers {
			questionID := strings.TrimSpace(entry.QuestionID)
			if questionID == "" {
				return errors.New("batch review answer requires question id")
			}
			if _, exists := seen[questionID]; exists {
				return fmt.Errorf("batch review answer repeats question %q", entry.QuestionID)
			}
			seen[questionID] = struct{}{}
			if err := validateReviewAnswerValue(entry.SelectedOptions, entry.Text); err != nil {
				return fmt.Errorf("batch review answer for question %q: %w", entry.QuestionID, err)
			}
		}
		return nil
	}
	if payload.PromptID == "" {
		return errors.New("review answer requires session and prompt")
	}
	return validateReviewAnswerValue(payload.SelectedOptions, payload.Text)
}

func validateReviewAnswerValue(selectedOptions []string, text string) error {
	if text == "" && len(selectedOptions) == 0 {
		return errors.New("answer requires text or selected options")
	}
	if text != "" && len(selectedOptions) != 0 {
		return errors.New("answer mixes text and selected options")
	}
	for _, option := range selectedOptions {
		if strings.TrimSpace(option) == "" {
			return errors.New("answer contains an empty option")
		}
	}
	return nil
}

func ReviewAnswerIdempotencyKey(sessionID ReviewSessionID, promptID ReviewPromptID, revision uint64) (string, error) {
	if sessionID == "" || promptID == "" || revision == 0 {
		return "", errors.New("review answer idempotency requires session, prompt and revision")
	}
	digest := sha256.Sum256([]byte(string(sessionID) + "\x00" + string(promptID) + "\x00" + fmt.Sprint(revision)))
	return "review.answer:" + hex.EncodeToString(digest[:]), nil
}

// SkillVerificationSyncPayload refreshes the skill verification catalog of one
// profile. Discovery never starts an attempt.
type SkillVerificationSyncPayload struct {
	ProfileID ProfileID `json:"profile_id"`
}

func (payload SkillVerificationSyncPayload) Validate() error {
	if payload.ProfileID == "" {
		return errors.New("skill verification sync requires profile")
	}
	return nil
}

func SkillVerificationSyncIdempotencyKey(profileID ProfileID, requestKey string) (string, error) {
	requestKey = strings.TrimSpace(requestKey)
	if profileID == "" || requestKey == "" {
		return "", errors.New("skill verification sync idempotency requires profile and request key")
	}
	digest := sha256.Sum256([]byte(string(profileID) + "\x00" + requestKey))
	return "skill_verification.sync:" + hex.EncodeToString(digest[:]), nil
}

// SkillVerificationStartPayload starts one explicitly selected qualification
// attempt. Starting may consume a limited or timed attempt.
type SkillVerificationStartPayload struct {
	ProfileID  ProfileID       `json:"profile_id"`
	Platform   Platform        `json:"platform"`
	OfferingID QualificationID `json:"offering_id"`
}

func (payload SkillVerificationStartPayload) Validate() error {
	if payload.ProfileID == "" || payload.Platform == "" || payload.OfferingID == "" {
		return errors.New("skill verification start requires profile, platform and offering")
	}
	return nil
}

func SkillVerificationStartIdempotencyKey(profileID ProfileID, offeringID QualificationID, requestKey string) (string, error) {
	requestKey = strings.TrimSpace(requestKey)
	if profileID == "" || offeringID == "" || requestKey == "" {
		return "", errors.New("skill verification start idempotency requires profile, offering and request key")
	}
	digest := sha256.Sum256([]byte(string(profileID) + "\x00" + string(offeringID) + "\x00" + requestKey))
	return "skill_verification.start:" + hex.EncodeToString(digest[:]), nil
}

type ResumeTouchPayload struct {
	ProfileID ProfileID `json:"profile_id"`
	ResumeID  string    `json:"resume_id"`
}

type ProfileActivityObservePayload struct {
	ProfileID ProfileID `json:"profile_id"`
	ResumeID  string    `json:"resume_id"`
}

// ProfileSessionRefreshPayload re-exports the live browser context into the
// profile storage state file so cookie rotations and session extensions do not
// leave the backend with a stale snapshot.
type ProfileSessionRefreshPayload struct {
	ProfileID ProfileID `json:"profile_id"`
}

func (payload ProfileSessionRefreshPayload) Validate() error {
	if payload.ProfileID == "" {
		return errors.New("profile session refresh requires profile id")
	}
	return nil
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

func (payload ProfileActivityObservePayload) Validate() error {
	if strings.TrimSpace(string(payload.ProfileID)) == "" || strings.TrimSpace(payload.ResumeID) == "" {
		return errors.New("profile activity observation payload requires profile and resume")
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

// ApplicationRetryIdempotencyKey scopes one operator retry request. A replayed
// request returns the already enqueued task instead of resetting the
// application and submitting it twice.
func ApplicationRetryIdempotencyKey(applicationID ApplicationID, requestKey string) (string, error) {
	requestKey = strings.TrimSpace(requestKey)
	if applicationID == "" || requestKey == "" {
		return "", errors.New("application retry idempotency requires application and request key")
	}
	digest := sha256.Sum256([]byte(string(applicationID) + "\x00" + requestKey))
	return "application.retry:" + hex.EncodeToString(digest[:]), nil
}

type ConversationSendPayload struct {
	ConversationID ConversationID `json:"conversation_id"`
	ReplyToID      MessageID      `json:"reply_to_id,omitempty"`
	Content        MessageContent `json:"content"`
}

type ConversationDiscoverPayload struct {
	ProfileID ProfileID `json:"profile_id"`
}

func (payload ConversationDiscoverPayload) Validate() error {
	if payload.ProfileID == "" {
		return errors.New("conversation discovery payload requires profile")
	}
	return nil
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

type ConversationFollowUpSelectPayload struct {
	ProfileID      ProfileID                 `json:"profile_id"`
	Strategy       FollowUpSelectionStrategy `json:"strategy"`
	MinimumSilence Duration                  `json:"minimum_silence"`
	RunAfter       Duration                  `json:"run_after"`
	DeadlineAfter  Duration                  `json:"deadline_after,omitempty"`
	Content        MessageContent            `json:"content"`
	Policy         FollowUpPolicy            `json:"policy"`
}

func (payload ConversationFollowUpPayload) Validate() error {
	if payload.FollowUpID == "" {
		return errors.New("conversation follow-up payload requires follow-up id")
	}
	return nil
}

func (payload ConversationFollowUpSelectPayload) Validate() error {
	if payload.ProfileID == "" {
		return errors.New("conversation follow-up selection requires profile")
	}
	if err := payload.Strategy.Validate(); err != nil {
		return err
	}
	if payload.MinimumSilence.Value() <= 0 {
		return errors.New("conversation follow-up selection requires positive minimum silence")
	}
	if payload.RunAfter.Value() <= 0 {
		return errors.New("conversation follow-up selection requires positive run-after duration")
	}
	if payload.DeadlineAfter.Value() < 0 || payload.DeadlineAfter.Value() > 0 && payload.DeadlineAfter.Value() <= payload.RunAfter.Value() {
		return errors.New("conversation follow-up deadline must be zero or greater than run-after duration")
	}
	if err := payload.Content.Validate(); err != nil {
		return err
	}
	if err := payload.Policy.Validate(); err != nil {
		return err
	}
	if !payload.Policy.CancelOnIncoming || !payload.Policy.RequireActiveConversation {
		return errors.New("selected follow-ups must cancel on incoming messages and require an active conversation")
	}
	if payload.Policy.Cooldown.Value() < payload.MinimumSilence.Value() {
		return errors.New("selected follow-up cooldown must not be shorter than minimum silence")
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

func ConversationSyncIdempotencyKey(conversationID ConversationID, requestKey string) (string, error) {
	if conversationID == "" || strings.TrimSpace(requestKey) == "" {
		return "", errors.New("conversation sync idempotency requires conversation and request key")
	}
	digest := sha256.Sum256([]byte(string(conversationID) + "\x00" + requestKey))
	return "conversation.sync:" + hex.EncodeToString(digest[:]), nil
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

// ResumeUpdatePayload applies one declared resume resource and optionally
// publishes the resume after a verified read-back.
type ResumeUpdatePayload struct {
	ProfileID   ProfileID `json:"profile_id"`
	ResourceTag string    `json:"resource_tag"`
	ResumeID    string    `json:"resume_id,omitempty"`
	Publish     bool      `json:"publish,omitempty"`
}

func (payload ResumeUpdatePayload) Validate() error {
	if payload.ProfileID == "" || strings.TrimSpace(payload.ResourceTag) == "" {
		return errors.New("resume update requires profile and resource")
	}
	if payload.Publish && strings.TrimSpace(payload.ResumeID) == "" {
		return errors.New("resume update with publish requires resume")
	}
	return nil
}

func ResumeUpdateIdempotencyKey(profileID ProfileID, resourceTag, requestKey string) (string, error) {
	resourceTag = strings.TrimSpace(resourceTag)
	requestKey = strings.TrimSpace(requestKey)
	if profileID == "" || resourceTag == "" || requestKey == "" {
		return "", errors.New("resume update idempotency requires profile, resource and request key")
	}
	digest := sha256.Sum256([]byte(string(profileID) + "\x00" + resourceTag + "\x00" + requestKey))
	return "resume.update:" + hex.EncodeToString(digest[:]), nil
}
