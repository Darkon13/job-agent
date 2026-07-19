package storage

import (
	"context"
	"errors"

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
