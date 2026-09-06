package adapter

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Darkon13/job-agent/core"
)

type Descriptor interface {
	Name() string
	Capabilities() []core.Capability
}

type VacancySearcher interface {
	ValidateSearch(query json.RawMessage) error
	Search(ctx context.Context, profileID core.ProfileID, query json.RawMessage, cursor string) (core.SearchPage, error)
}

// VacancyReader loads the current, full platform representation before an
// application decision is made. Search results are deliberately insufficient
// for this step because platforms omit requirements and applicant relations.
type VacancyReader interface {
	ReadVacancy(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) (core.Vacancy, error)
}

// SuitableResume is the platform-neutral identity needed to choose which
// profile resume can be used for one vacancy. Full resume contents stay in the
// platform adapter.
type SuitableResume struct {
	ID    string
	Title string
}

type SuitableResumeReader interface {
	ListSuitableResumes(ctx context.Context, profileID core.ProfileID, key core.VacancyKey) ([]SuitableResume, error)
}

// ProfileReadResult is the minimal, non-secret identity returned by an
// authenticated platform probe. Platform payloads and credentials stay inside
// the adapter.
type ProfileReadResult struct {
	ExternalAccountID string
	AuthType          string
}

type ProfileReader interface {
	ReadProfile(ctx context.Context, profileID core.ProfileID) (ProfileReadResult, error)
}

// ProfileReaderFactory binds a secret reference to one profile. The returned
// reader is profile-scoped so adapters can serialize refresh and session
// checks without a global lock shared by unrelated accounts.
type ProfileReaderFactory interface {
	NewProfileReader(profileID core.ProfileID, credentialsRef string) (ProfileReader, error)
}

// BrowserSessionBinder binds a previously exported browser storage state to
// read-only platform operations. It deliberately returns only a vacancy
// reader: a browser session must not silently become permission to submit an
// application through an API transport.
type BrowserSessionBinder interface {
	BindBrowserSession(profileID core.ProfileID, stateFile string) (VacancyReader, error)
}

type ConversationSendCommand struct {
	ProfileID              core.ProfileID
	ConversationID         core.ConversationID
	ExternalConversationID string
	ReplyToID              core.MessageID
	Text                   string
	IdempotencyKey         string
}

type ConversationSyncResult struct {
	Messages   []core.ConversationMessage
	ObservedAt time.Time
}

type ConversationTransport interface {
	SendConversationMessage(ctx context.Context, command ConversationSendCommand) (core.ConversationMessage, error)
	MarkConversationRead(ctx context.Context, profileID core.ProfileID, externalConversationID string) error
	SyncConversation(ctx context.Context, profileID core.ProfileID, conversationID core.ConversationID, externalConversationID string) (ConversationSyncResult, error)
}

type ApplicationSubmitCommand struct {
	ProfileID      core.ProfileID
	Vacancy        core.VacancyKey
	ResumeID       string
	Message        string
	IdempotencyKey string
}

type ApplicationSubmitResult struct {
	ExternalNegotiationID string
	AlreadyApplied        bool
}

type ApplicationTransport interface {
	SubmitApplication(ctx context.Context, command ApplicationSubmitCommand) (ApplicationSubmitResult, error)
}

type ApplicationReconcileCommand struct {
	ProfileID core.ProfileID
	Vacancy   core.VacancyKey
	ResumeID  string
}

type ApplicationReconcileResult struct {
	Applied               bool
	ExternalNegotiationID string
}

type ApplicationReconciler interface {
	ReconcileApplication(ctx context.Context, command ApplicationReconcileCommand) (ApplicationReconcileResult, error)
}

type ResumePublishCommand struct {
	ProfileID      core.ProfileID
	ResumeID       string
	IdempotencyKey string
}

type ResumePublishResult struct {
	NextPublishAt *time.Time
}

type ResumePublisher interface {
	PublishResume(ctx context.Context, command ResumePublishCommand) (ResumePublishResult, error)
}

type ResumeTouchCommand struct {
	ProfileID      core.ProfileID
	ResumeID       string
	IdempotencyKey string
}

type ResumeTouchResult struct {
	NextTouchAt *time.Time
}

type ResumeToucher interface {
	TouchResume(ctx context.Context, command ResumeTouchCommand) (ResumeTouchResult, error)
}

// Adapter is a facade over all transports used by one platform. Workflows
// depend on the smaller capability interfaces instead of this full facade.
type Adapter interface {
	Descriptor
	VacancySearcher
}

type Factory func(config json.RawMessage) (Adapter, error)
