package sqlite

import (
	"context"
	"time"

	"github.com/Darkon13/job-agent/storage"
)

func (store *Store) QueryApplications(ctx context.Context, query storage.ApplicationQuery) (storage.ApplicationPage, error) {
	if err := query.Validate(); err != nil {
		return storage.ApplicationPage{}, err
	}
	rows, err := store.db.QueryContext(ctx, `SELECT a.id, a.profile_id, a.status, a.decision_code,
		COALESCE(s.disposition, ''), a.external_id, COALESCE(v.title, ''), COALESCE(v.employer, ''), a.updated_at
		FROM applications a LEFT JOIN vacancies v ON v.platform = a.platform AND v.external_id = a.external_id
		LEFT JOIN application_platform_states s ON s.application_id = a.id
		WHERE (? = '' OR a.profile_id = ?) AND (? = '' OR a.status = ?) AND (? = '' OR a.decision_code = ?)
		  AND (? = '' OR a.external_id = ?)`,
		query.ProfileID, query.ProfileID, query.Status, query.Status, query.DecisionCode, query.DecisionCode,
		query.VacancyExternalID, query.VacancyExternalID)
	if err != nil {
		return storage.ApplicationPage{}, err
	}
	defer rows.Close()
	entries := make([]storage.ApplicationListEntry, 0)
	for rows.Next() {
		var entry storage.ApplicationListEntry
		var updatedAt int64
		if err := rows.Scan(&entry.ID, &entry.ProfileID, &entry.Status, &entry.DecisionCode, &entry.Disposition, &entry.ExternalID, &entry.Title, &entry.Employer, &updatedAt); err != nil {
			return storage.ApplicationPage{}, err
		}
		entry.UpdatedAt = time.Unix(0, updatedAt).UTC()
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return storage.ApplicationPage{}, err
	}
	return storage.SelectApplicationPage(entries, query), nil
}
