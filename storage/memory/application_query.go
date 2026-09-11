package memory

import (
	"context"
	"github.com/Darkon13/job-agent/storage"
)

func (repository *Repository) QueryApplications(ctx context.Context, query storage.ApplicationQuery) (storage.ApplicationPage, error) {
	if err := ctx.Err(); err != nil {
		return storage.ApplicationPage{}, err
	}
	if err := query.Validate(); err != nil {
		return storage.ApplicationPage{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	entries := make([]storage.ApplicationListEntry, 0, len(repository.applications))
	for _, application := range repository.applications {
		vacancy := repository.vacancies[application.Key.Vacancy]
		entries = append(entries, storage.ApplicationListEntry{ID: application.ID, ProfileID: application.Key.ProfileID, Status: application.Status,
			DecisionCode: application.DecisionCode, Disposition: repository.applicationStates[application.ID].Disposition,
			Title: vacancy.Title, Employer: vacancy.Employer, UpdatedAt: application.UpdatedAt})
	}
	return storage.SelectApplicationPage(entries, query), nil
}
