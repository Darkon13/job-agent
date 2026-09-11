package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var _ storage.QualificationCatalogRepository = (*Store)(nil)

func (store *Store) UpsertQualificationOfferings(ctx context.Context, platform core.Platform, profileID core.ProfileID, offerings []core.QualificationOffering) error {
	for _, offering := range offerings {
		if err := offering.Validate(); err != nil {
			return err
		}
		if offering.Platform != platform || offering.ProfileID != profileID {
			return fmt.Errorf("qualification offering %s belongs to another profile", offering.ID)
		}
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin qualification offerings upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, offering := range offerings {
		var levelOrder sql.NullInt64
		if offering.Qualification.LevelOrder != nil {
			levelOrder = sql.NullInt64{Int64: int64(*offering.Qualification.LevelOrder), Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO qualification_offerings
			(platform, profile_id, offering_id, external_id, family_id, family_name,
			 level_id, level_name, level_order, status, observed_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(platform, profile_id, offering_id) DO UPDATE SET
				external_id = excluded.external_id,
				family_name = excluded.family_name, level_name = excluded.level_name,
				level_order = excluded.level_order, status = excluded.status,
				observed_at = MAX(observed_at, excluded.observed_at), updated_at = excluded.updated_at`,
			offering.Platform, offering.ProfileID, offering.ID, offering.ExternalID,
			offering.Qualification.FamilyID, offering.Qualification.FamilyName,
			offering.Qualification.LevelID, offering.Qualification.LevelName, levelOrder,
			offering.Status, offering.ObservedAt.UnixNano(), time.Now().UTC().UnixNano()); err != nil {
			return fmt.Errorf("upsert qualification offering %s: %w", offering.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit qualification offerings: %w", err)
	}
	return nil
}

func (store *Store) QualificationOfferings(ctx context.Context, platform core.Platform, profileID core.ProfileID) ([]core.QualificationOffering, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT offering_id, external_id, family_id, family_name,
		level_id, level_name, level_order, status, observed_at
		FROM qualification_offerings WHERE platform = ? AND profile_id = ?
		ORDER BY family_id, level_order, level_id, offering_id`, platform, profileID)
	if err != nil {
		return nil, fmt.Errorf("list qualification offerings: %w", err)
	}
	defer rows.Close()
	offerings := make([]core.QualificationOffering, 0)
	for rows.Next() {
		offering := core.QualificationOffering{Platform: platform, ProfileID: profileID}
		var levelOrder sql.NullInt64
		var observedAt int64
		if err := rows.Scan(&offering.ID, &offering.ExternalID, &offering.Qualification.FamilyID,
			&offering.Qualification.FamilyName, &offering.Qualification.LevelID,
			&offering.Qualification.LevelName, &levelOrder, &offering.Status, &observedAt); err != nil {
			return nil, fmt.Errorf("scan qualification offering: %w", err)
		}
		if levelOrder.Valid {
			order := int(levelOrder.Int64)
			offering.Qualification.LevelOrder = &order
		}
		offering.ObservedAt = time.Unix(0, observedAt).UTC()
		best, found, err := loadBestQualification(ctx, store.db, core.QualificationAttempt{
			Platform: platform, ProfileID: profileID, Qualification: offering.Qualification,
		})
		if err != nil {
			return nil, err
		}
		if found {
			value := best
			offering.BestResult = &value
		}
		if err := offering.Validate(); err != nil {
			return nil, err
		}
		offerings = append(offerings, offering)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return offerings, nil
}
