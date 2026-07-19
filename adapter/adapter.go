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
}

type ApplicationTransport interface {
	SubmitApplication(ctx context.Context, command ApplicationSubmitCommand) (ApplicationSubmitResult, error)
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

// Adapter is a facade over all transports used by one platform. Workflows
// depend on the smaller capability interfaces instead of this full facade.
type Adapter interface {
	Descriptor
	VacancySearcher
}

type Factory func(config json.RawMessage) (Adapter, error)
