package storage

import (
	"context"
	"errors"
	"time"

	"github.com/Darkon13/job-agent/core"
)

var ErrRevisionConflict = errors.New("repository revision conflict")

type VacancyRepository interface {
	UpsertVacancy(ctx context.Context, vacancy core.Vacancy) (created bool, err error)
	RecordDiscovery(ctx context.Context, discovery core.VacancyDiscovery) (created bool, err error)
}

type ApplicationRepository interface {
	// CreateApplication returns the already stored application when the unique
	// profile/vacancy key exists. Callers must use the returned ID.
	CreateApplication(ctx context.Context, candidate core.Application) (stored core.Application, created bool, err error)
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

type ReviewRepository interface {
	CreateReviewSession(ctx context.Context, session core.ReviewSession) (created bool, err error)
	ReviewSession(ctx context.Context, id core.ReviewSessionID) (core.ReviewSession, error)
	ReviewPrompt(ctx context.Context, id core.ReviewPromptID) (core.ReviewPrompt, error)
	SaveReviewPrompt(ctx context.Context, session core.ReviewSession, prompt core.ReviewPrompt, expectedRevision uint64) error
	AppendReviewSelection(ctx context.Context, session core.ReviewSession, selection core.ReviewSelection, expectedRevision uint64) error
	ReviewSelections(ctx context.Context, sessionID core.ReviewSessionID) ([]core.ReviewSelection, error)
}

type ConversationFilter struct {
	Platform  core.Platform
	ProfileID core.ProfileID
	Status    core.ConversationStatus
}

type FollowUpFilter struct {
	ConversationID core.ConversationID
	ProfileID      core.ProfileID
	Status         core.FollowUpStatus
	DueBefore      *time.Time
}

type ConversationRepository interface {
	CreateConversation(ctx context.Context, candidate core.Conversation) (stored core.Conversation, created bool, err error)
	Conversation(ctx context.Context, id core.ConversationID) (core.Conversation, error)
	ListConversations(ctx context.Context, filter ConversationFilter) ([]core.Conversation, error)
	AppendConversationMessage(ctx context.Context, message core.ConversationMessage, observedAt time.Time) (conversation core.Conversation, created bool, err error)
	ConversationMessages(ctx context.Context, id core.ConversationID) ([]core.ConversationMessage, error)
	CreateFollowUp(ctx context.Context, candidate core.FollowUp) (stored core.FollowUp, created bool, err error)
	FollowUp(ctx context.Context, id core.FollowUpID) (core.FollowUp, error)
	SaveFollowUp(ctx context.Context, candidate core.FollowUp, expectedRevision uint64) error
	ListFollowUps(ctx context.Context, filter FollowUpFilter) ([]core.FollowUp, error)
}
