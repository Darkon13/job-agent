package storage

import (
	"context"
	"errors"
	"time"

	"github.com/Darkon13/job-agent/core"
)

var (
	ErrRevisionConflict             = errors.New("repository revision conflict")
	ErrProfileMutationLocked        = errors.New("profile already has an active mutation workflow")
	ErrApplicationTailoringNotFound = errors.New("application tailoring not found")
	ErrAuthSessionNotFound          = errors.New("auth session not found")
	ErrApplicationRemoved           = errors.New("application was removed from the working set")
)

// RuntimeStats is an aggregate view intended for health checks and operator
// dashboards. It deliberately contains counts only and never task payloads,
// message bodies, profile credentials or external identifiers.
type RuntimeStats struct {
	SearchRuns            int `json:"search_runs"`
	Vacancies             int `json:"vacancies"`
	Discoveries           int `json:"discoveries"`
	Applications          int `json:"applications"`
	ApplicationCampaigns  int `json:"application_campaigns"`
	CampaignApplications  int `json:"campaign_applications"`
	ApplicationTailorings int `json:"application_tailorings"`
	Tasks                 int `json:"tasks"`
	TestDefinitions       int `json:"test_definitions"`
	ReviewSessions        int `json:"review_sessions"`
	ReviewSelections      int `json:"review_selections"`
	Conversations         int `json:"conversations"`
	Messages              int `json:"messages"`
	FollowUps             int `json:"follow_ups"`
	ProfileStateProposals int `json:"profile_state_proposals"`
	ProfileStateRevisions int `json:"profile_state_revisions"`
	ProfileActivity       int `json:"profile_activity"`
	ActivitySnapshots     int `json:"activity_snapshots"`
}

type TaskCount struct {
	Type     core.TaskType     `json:"type"`
	Status   core.TaskStatus   `json:"status"`
	Priority core.TaskPriority `json:"priority"`
	Count    int               `json:"count"`
}

// FailedTaskSummary is an operator-safe task view. It intentionally excludes
// payload, source, idempotency key and correlation data.
type FailedTaskSummary struct {
	ID        core.TaskID      `json:"id"`
	Type      core.TaskType    `json:"type"`
	ProfileID core.ProfileID   `json:"profile_id,omitempty"`
	Attempts  int              `json:"attempts"`
	Failure   core.TaskFailure `json:"failure"`
	UpdatedAt time.Time        `json:"updated_at"`
}

type FailedTaskRepository interface {
	ListFailedTasks(ctx context.Context, limit int) ([]FailedTaskSummary, error)
}

type ApplicationCount struct {
	ProfileID    core.ProfileID         `json:"profile_id,omitempty"`
	Status       core.ApplicationStatus `json:"status"`
	DecisionCode string                 `json:"decision_code,omitempty"`
	Count        int                    `json:"count"`
}

type ApplicationFilter struct {
	ProfileID core.ProfileID
	Status    core.ApplicationStatus
	Limit     int
}

// ApplicationReadRepository exposes application objects to operator-facing
// read models without widening the workflow write contract.
type ApplicationReadRepository interface {
	ApplicationByID(ctx context.Context, id core.ApplicationID) (core.Application, error)
	ListApplications(ctx context.Context, filter ApplicationFilter) ([]core.Application, error)
}

type ProfileActivityFilter struct {
	Platform  core.Platform
	ProfileID core.ProfileID
	Kind      core.ProfileActivityKind
}

type ProfileActivityCount struct {
	Platform       core.Platform            `json:"platform"`
	ProfileID      core.ProfileID           `json:"profile_id"`
	Kind           core.ProfileActivityKind `json:"kind"`
	Count          int                      `json:"count"`
	LastOccurredAt time.Time                `json:"last_occurred_at"`
}

type ProfileActivityRepository interface {
	RecordProfileActivity(ctx context.Context, candidate core.ProfileActivityRecord) (created bool, err error)
	ListProfileActivity(ctx context.Context, filter ProfileActivityFilter) ([]core.ProfileActivityRecord, error)
	ProfileActivityCounts(ctx context.Context, filter ProfileActivityFilter) ([]ProfileActivityCount, error)
}

type ProfileActivitySnapshotFilter struct {
	Platform  core.Platform
	ProfileID core.ProfileID
	ResumeID  string
	Limit     int
}

type ProfileActivitySnapshotRepository interface {
	RecordProfileActivitySnapshot(ctx context.Context, candidate core.ProfileActivitySnapshot) (created bool, err error)
	ListProfileActivitySnapshots(ctx context.Context, filter ProfileActivitySnapshotFilter) ([]core.ProfileActivitySnapshot, error)
}

// ErrConversationMessageConflict reports that a platform message with the
// same identity is already stored with different content. Sync callers skip
// such messages instead of failing the whole conversation sync.
var ErrConversationMessageConflict = errors.New("conversation message identity conflicts with different content")

type SearchRunRepository interface {
	CreateSearchRun(ctx context.Context, candidate core.SearchRun) (stored core.SearchRun, created bool, err error)
	SearchRun(ctx context.Context, searchID core.SearchID) (core.SearchRun, error)
	SaveSearchRun(ctx context.Context, candidate core.SearchRun, expectedRevision uint64) error
}

type ApplicationCampaignRepository interface {
	CreateApplicationCampaign(ctx context.Context, candidate core.ApplicationCampaign) (stored core.ApplicationCampaign, created bool, err error)
	ApplicationCampaign(ctx context.Context, id core.ApplicationCampaignID) (core.ApplicationCampaign, error)
	ListApplicationCampaigns(ctx context.Context, limit int) ([]core.ApplicationCampaign, error)
	SaveApplicationCampaign(ctx context.Context, candidate core.ApplicationCampaign, expectedRevision uint64) error
	LinkCampaignApplication(ctx context.Context, item core.CampaignApplication) (created bool, err error)
	ListCampaignApplications(ctx context.Context, id core.ApplicationCampaignID) ([]core.CampaignApplication, error)
	ListCampaignApplicationStates(ctx context.Context, id core.ApplicationCampaignID) ([]core.CampaignApplicationState, error)
}

type VacancyRepository interface {
	UpsertVacancy(ctx context.Context, vacancy core.Vacancy) (created bool, err error)
	Vacancy(ctx context.Context, key core.VacancyKey) (core.Vacancy, error)
	RecordDiscovery(ctx context.Context, discovery core.VacancyDiscovery) (created bool, err error)
}

type ApplicationRepository interface {
	// CreateApplication returns the already stored application when the unique
	// profile/vacancy key exists. Callers must use the returned ID.
	CreateApplication(ctx context.Context, candidate core.Application) (stored core.Application, created bool, err error)
	Application(ctx context.Context, key core.ApplicationKey) (core.Application, error)
	SaveApplication(ctx context.Context, candidate core.Application, expectedStatus core.ApplicationStatus) error
}

type ApplicationRemovalRepository interface {
	ApplicationTombstone(ctx context.Context, id core.ApplicationID) (core.ApplicationTombstone, bool, error)
	RemoveApplication(ctx context.Context, id core.ApplicationID, request core.ApplicationRemoval, removedAt time.Time) (tombstone core.ApplicationTombstone, removed bool, err error)
}

type ApplicationPlatformStateRepository interface {
	SaveApplicationPlatformState(ctx context.Context, state core.ApplicationPlatformState) error
	ApplicationPlatformState(ctx context.Context, applicationID core.ApplicationID) (core.ApplicationPlatformState, error)
	ListApplicationsForRetention(ctx context.Context, profileID core.ProfileID) ([]core.Application, error)
	ListStaleValidationApplications(ctx context.Context, profileID core.ProfileID) ([]core.Application, error)
}

type ApplicationBudgetRepository interface {
	ReserveApplicationBudget(ctx context.Context, params core.ReserveApplicationBudgetParams) (core.ApplicationBudgetReservation, error)
	CommitApplicationBudget(ctx context.Context, applicationID core.ApplicationID, now time.Time) error
	ReleaseApplicationBudget(ctx context.Context, applicationID core.ApplicationID, now time.Time) error
	ApplicationBudgetUsage(ctx context.Context, profileID core.ProfileID, platform core.Platform, windowStart time.Time) (core.ApplicationBudgetUsage, error)
}

type ApplicationPaceRepository interface {
	// AcquireApplicationPace returns allowed=false with a future ScheduledAt
	// when the task must be released and retried without performing a submit.
	AcquireApplicationPace(ctx context.Context, params core.AcquireApplicationPaceParams) (reservation core.ApplicationPaceReservation, allowed bool, err error)
}

type ApplicationTailoringRepository interface {
	CreateApplicationTailoring(ctx context.Context, candidate core.ApplicationTailoring) (stored core.ApplicationTailoring, created bool, err error)
	ApplicationTailoring(ctx context.Context, id core.ApplicationTailoringID) (core.ApplicationTailoring, error)
	ApplicationTailoringByApplication(ctx context.Context, applicationID core.ApplicationID) (core.ApplicationTailoring, error)
	SaveApplicationTailoring(ctx context.Context, candidate core.ApplicationTailoring, expectedRevision uint64) error
}

type AuthSessionRepository interface {
	CreateAuthSession(ctx context.Context, candidate core.AuthSession) (stored core.AuthSession, created bool, err error)
	AuthSession(ctx context.Context, id core.AuthSessionID) (core.AuthSession, error)
	SaveAuthSession(ctx context.Context, candidate core.AuthSession, expectedRevision uint64) error
}

type ProfileStateProposalFilter struct {
	ResourceTag string
	ProfileID   core.ProfileID
	Status      core.ProfileStateProposalStatus
}

type ProfileStateProposalRepository interface {
	CreateProfileStateProposal(ctx context.Context, candidate core.ProfileStateProposal) (stored core.ProfileStateProposal, created bool, err error)
	ProfileStateProposal(ctx context.Context, id core.ProfileStateProposalID) (core.ProfileStateProposal, error)
	ListProfileStateProposals(ctx context.Context, filter ProfileStateProposalFilter) ([]core.ProfileStateProposal, error)
}

type ProfileStateRevisionFilter struct {
	ResourceTag string
	ProfileID   core.ProfileID
	Limit       int
}

// ProfileStateRevisionRepository stores the redacted history of confirmed
// profile state applies. One proposal produces at most one revision.
type ProfileStateRevisionRepository interface {
	CreateProfileStateRevision(ctx context.Context, candidate core.ProfileStateRevision) (stored core.ProfileStateRevision, created bool, err error)
	ListProfileStateRevisions(ctx context.Context, filter ProfileStateRevisionFilter) ([]core.ProfileStateRevision, error)
}

type TestDefinitionFilter struct {
	Platform core.Platform
	FamilyID string
	LevelID  string
}

type TestCatalogRepository interface {
	UpsertTestDefinition(ctx context.Context, definition core.TestDefinition) (created bool, err error)
	TestDefinition(ctx context.Context, id core.TestDefinitionID) (core.TestDefinition, error)
	ListTestDefinitions(ctx context.Context, filter TestDefinitionFilter) ([]core.TestDefinition, error)
}

type ReviewSessionFilter struct {
	Status    core.ReviewSessionStatus
	ProfileID core.ProfileID
	Platform  core.Platform
	Query     string
	Limit     int
	Offset    int
}

type ReviewRepository interface {
	CreateReviewSession(ctx context.Context, session core.ReviewSession) (created bool, err error)
	ReviewSession(ctx context.Context, id core.ReviewSessionID) (core.ReviewSession, error)
	ListReviewSessions(ctx context.Context, filter ReviewSessionFilter) ([]core.ReviewSession, error)
	CancelReviewSession(ctx context.Context, session core.ReviewSession, expectedRevision uint64) error
	ReviewPrompt(ctx context.Context, id core.ReviewPromptID) (core.ReviewPrompt, error)
	SaveReviewPrompt(ctx context.Context, session core.ReviewSession, prompt core.ReviewPrompt, expectedRevision uint64) error
	AppendReviewSelection(ctx context.Context, session core.ReviewSession, selection core.ReviewSelection, expectedRevision uint64) error
	ReviewSelections(ctx context.Context, sessionID core.ReviewSessionID) ([]core.ReviewSelection, error)
}

// TestAttemptRepository stores the latest vacancy test outcome per profile.
// Attempts are monotonic: a passed attempt is never downgraded by a later
// failure, and retries with the same fingerprint do not inflate the counter.
type TestAttemptRepository interface {
	SaveTestAttempt(ctx context.Context, attempt core.TestAttempt) error
	LatestTestAttempt(ctx context.Context, platform core.Platform, profileID core.ProfileID, externalID string) (core.TestAttempt, bool, error)
}

// AnswerBlockRevisionRepository stores append-only reviewed answer revisions.
// Append assigns the next revision number for the block tag.
type AnswerBlockRevisionRepository interface {
	AppendAnswerBlockRevision(ctx context.Context, revision core.AnswerBlockRevision) (core.AnswerBlockRevision, error)
	LatestAnswerBlockRevision(ctx context.Context, tag string) (core.AnswerBlockRevision, bool, error)
}

// QualificationRepository stores every completed skill verification attempt
// and the monotonic best result per platform, profile, family and level.
type QualificationRepository interface {
	SaveQualificationAttempt(ctx context.Context, attempt core.QualificationAttempt, recordedAt time.Time) (core.QualificationResult, bool, error)
	QualificationAttempts(ctx context.Context, platform core.Platform, profileID core.ProfileID, familyID, levelID string) ([]core.QualificationAttempt, error)
	BestQualificationResult(ctx context.Context, platform core.Platform, profileID core.ProfileID, familyID, levelID string) (core.QualificationResult, bool, error)
}

// QualificationCatalogRepository stores the skill verification catalog
// observed for a profile.
type QualificationCatalogRepository interface {
	UpsertQualificationOfferings(ctx context.Context, platform core.Platform, profileID core.ProfileID, offerings []core.QualificationOffering) error
	QualificationOfferings(ctx context.Context, platform core.Platform, profileID core.ProfileID) ([]core.QualificationOffering, error)
}

type ConversationFilter struct {
	Platform  core.Platform
	ProfileID core.ProfileID
	Status    core.ConversationStatus
	Query     string
	// UnreadOnly and QuestionnaireOnly mirror the dashboard filters.
	UnreadOnly        bool
	QuestionnaireOnly bool
	Limit             int
	Offset            int
}

// ConversationCounts summarizes a filtered conversation set without loading it.
type ConversationCounts struct {
	Total  int `json:"total"`
	Unread int `json:"unread"`
	Active int `json:"active"`
}

type FollowUpFilter struct {
	ConversationID core.ConversationID
	ProfileID      core.ProfileID
	Status         core.FollowUpStatus
	DueBefore      *time.Time
}

// ConversationPurgeRepository removes conversations whose application left the
// working set; the application retention job uses it after removals.
type ConversationPurgeRepository interface {
	PurgeOrphanConversations(ctx context.Context, profileID core.ProfileID) (int, error)
}

type ConversationRepository interface {
	CreateConversation(ctx context.Context, candidate core.Conversation) (stored core.Conversation, created bool, err error)
	Conversation(ctx context.Context, id core.ConversationID) (core.Conversation, error)
	SaveConversation(ctx context.Context, candidate core.Conversation, expectedRevision uint64) error
	ListConversations(ctx context.Context, filter ConversationFilter) ([]core.Conversation, error)
	CountConversations(ctx context.Context, filter ConversationFilter) (ConversationCounts, error)
	AppendConversationMessage(ctx context.Context, message core.ConversationMessage, observedAt time.Time) (conversation core.Conversation, created bool, err error)
	ConversationMessages(ctx context.Context, id core.ConversationID) ([]core.ConversationMessage, error)
	CreateFollowUp(ctx context.Context, candidate core.FollowUp) (stored core.FollowUp, created bool, err error)
	FollowUp(ctx context.Context, id core.FollowUpID) (core.FollowUp, error)
	SaveFollowUp(ctx context.Context, candidate core.FollowUp, expectedRevision uint64) error
	ListFollowUps(ctx context.Context, filter FollowUpFilter) ([]core.FollowUp, error)
	// ConversationsAwaitingQuestionnaire lists conversations of the profile
	// whose inbound questionnaire prompt has no later outgoing reply. The
	// scheduled discovery window does not enumerate the whole platform
	// catalog, so the scheduler uses this to keep locally known pending
	// prompts in sync instead of waiting for an operator to open the chat.
	ConversationsAwaitingQuestionnaire(ctx context.Context, profileID core.ProfileID, since time.Time, limit int) ([]core.ConversationID, error)
	// OpenQuestionnaireConversationIDs lists conversations where a questionnaire
	// is still running: the latest prompt is not closed by PARTICIPANT_LEFT.
	OpenQuestionnaireConversationIDs(ctx context.Context) ([]core.ConversationID, error)
	// AttachConversationApplication links a chat to its application once. The
	// identity is immutable through SaveConversation, so the link has its own
	// operation.
	AttachConversationApplication(ctx context.Context, id core.ConversationID, applicationID core.ApplicationID) (bool, error)
	// MarkConversationsReadLocally marks every unread chat read in one step; an
	// empty profile sweeps every account. The platform mark-read stays a
	// per-chat best effort.
	MarkConversationsReadLocally(ctx context.Context, profileID core.ProfileID, now time.Time) (int, error)
	// ApplicationConversations lists the chats linked to one application.
	ApplicationConversations(ctx context.Context, applicationID core.ApplicationID) ([]core.Conversation, error)
}
