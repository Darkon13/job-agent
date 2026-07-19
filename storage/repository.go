package storage

import (
	"context"

	"github.com/Darkon13/job-agent/core"
)

type VacancyRepository interface {
	UpsertVacancy(ctx context.Context, vacancy core.Vacancy) (created bool, err error)
	RecordDiscovery(ctx context.Context, discovery core.VacancyDiscovery) (created bool, err error)
}

type ApplicationRepository interface {
	// CreateApplication returns the already stored application when the unique
	// profile/vacancy key exists. Callers must use the returned ID.
	CreateApplication(ctx context.Context, candidate core.Application) (stored core.Application, created bool, err error)
}
