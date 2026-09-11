package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var _ storage.QualificationRepository = (*Store)(nil)

func (store *Store) SaveQualificationAttempt(ctx context.Context, attempt core.QualificationAttempt, recordedAt time.Time) (core.QualificationResult, bool, error) {
	if err := attempt.Validate(); err != nil {
		return core.QualificationResult{}, false, err
	}
	if recordedAt.IsZero() {
		return core.QualificationResult{}, false, errors.New("qualification attempt requires recorded_at")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.QualificationResult{}, false, fmt.Errorf("begin qualification attempt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	current, exists, err := loadBestQualification(ctx, tx, attempt)
	if err != nil {
		return core.QualificationResult{}, false, err
	}
	var currentPtr *core.QualificationResult
	if exists {
		currentPtr = &current
	}
	best, promoted, err := core.PreferQualificationResult(currentPtr, attempt.Result)
	if err != nil {
		return core.QualificationResult{}, false, err
	}
	order := qualificationOrder(attempt.Qualification)
	score, maxScore := qualificationScores(attempt.Result)
	if _, err := tx.ExecContext(ctx, `INSERT INTO qualification_attempts
		(platform, profile_id, family_id, family_name, level_id, level_name, level_order,
		 status, score, max_score, verified, answer_block_tag, attempt_fingerprint, completed_at, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.Platform, attempt.ProfileID, attempt.Qualification.FamilyID, attempt.Qualification.FamilyName,
		attempt.Qualification.LevelID, attempt.Qualification.LevelName, order,
		attempt.Result.Status, score, maxScore, attempt.Result.Verified, attempt.Result.AnswerBlockTag,
		attempt.AttemptFingerprint, attempt.Result.CompletedAt.UnixNano(), recordedAt.UnixNano()); err != nil {
		return core.QualificationResult{}, false, fmt.Errorf("insert qualification attempt: %w", err)
	}
	if promoted {
		if _, err := tx.ExecContext(ctx, `INSERT INTO qualification_best_results
			(platform, profile_id, family_id, level_id, status, score, max_score, verified, answer_block_tag, completed_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(platform, profile_id, family_id, level_id) DO UPDATE SET
				status = excluded.status, score = excluded.score, max_score = excluded.max_score,
				verified = excluded.verified, answer_block_tag = excluded.answer_block_tag,
				completed_at = excluded.completed_at, updated_at = excluded.updated_at`,
			attempt.Platform, attempt.ProfileID, attempt.Qualification.FamilyID, attempt.Qualification.LevelID,
			best.Status, score, maxScore, best.Verified, best.AnswerBlockTag,
			best.CompletedAt.UnixNano(), recordedAt.UnixNano()); err != nil {
			return core.QualificationResult{}, false, fmt.Errorf("promote qualification result: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return core.QualificationResult{}, false, fmt.Errorf("commit qualification attempt: %w", err)
	}
	if !promoted && !exists {
		return core.QualificationResult{}, false, nil
	}
	return best, promoted, nil
}

func (store *Store) BestQualificationResult(ctx context.Context, platform core.Platform, profileID core.ProfileID, familyID, levelID string) (core.QualificationResult, bool, error) {
	attempt := core.QualificationAttempt{
		Platform: platform, ProfileID: profileID,
		Qualification: core.QualificationDescriptor{FamilyID: familyID, LevelID: levelID},
	}
	return loadBestQualification(ctx, store.db, attempt)
}

type bestQualificationQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func loadBestQualification(ctx context.Context, querier bestQualificationQuerier, attempt core.QualificationAttempt) (core.QualificationResult, bool, error) {
	row := querier.QueryRowContext(ctx, `SELECT status, score, max_score, verified, answer_block_tag, completed_at
		FROM qualification_best_results WHERE platform = ? AND profile_id = ? AND family_id = ? AND level_id = ?`,
		attempt.Platform, attempt.ProfileID, attempt.Qualification.FamilyID, attempt.Qualification.LevelID)
	var result core.QualificationResult
	var score, maxScore sql.NullFloat64
	var completedAt int64
	if err := row.Scan(&result.Status, &score, &maxScore, &result.Verified, &result.AnswerBlockTag, &completedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.QualificationResult{}, false, nil
		}
		return core.QualificationResult{}, false, fmt.Errorf("load best qualification result: %w", err)
	}
	if score.Valid && maxScore.Valid {
		value, maximum := score.Float64, maxScore.Float64
		result.Score, result.MaxScore = &value, &maximum
	}
	result.CompletedAt = time.Unix(0, completedAt).UTC()
	if err := result.Validate(); err != nil {
		return core.QualificationResult{}, false, err
	}
	return result, true, nil
}

func (store *Store) QualificationAttempts(ctx context.Context, platform core.Platform, profileID core.ProfileID, familyID, levelID string) ([]core.QualificationAttempt, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT family_name, level_name, level_order,
		status, score, max_score, verified, answer_block_tag, attempt_fingerprint, completed_at
		FROM qualification_attempts
		WHERE platform = ? AND profile_id = ? AND family_id = ? AND level_id = ?
		ORDER BY completed_at, id`, platform, profileID, familyID, levelID)
	if err != nil {
		return nil, fmt.Errorf("list qualification attempts: %w", err)
	}
	defer rows.Close()
	attempts := make([]core.QualificationAttempt, 0)
	for rows.Next() {
		attempt := core.QualificationAttempt{
			Platform: platform, ProfileID: profileID,
			Qualification: core.QualificationDescriptor{FamilyID: familyID, LevelID: levelID},
		}
		var levelOrder sql.NullInt64
		var score, maxScore sql.NullFloat64
		var completedAt int64
		if err := rows.Scan(&attempt.Qualification.FamilyName, &attempt.Qualification.LevelName, &levelOrder,
			&attempt.Result.Status, &score, &maxScore, &attempt.Result.Verified,
			&attempt.Result.AnswerBlockTag, &attempt.AttemptFingerprint, &completedAt); err != nil {
			return nil, fmt.Errorf("scan qualification attempt: %w", err)
		}
		if levelOrder.Valid {
			order := int(levelOrder.Int64)
			attempt.Qualification.LevelOrder = &order
		}
		if score.Valid && maxScore.Valid {
			value, maximum := score.Float64, maxScore.Float64
			attempt.Result.Score, attempt.Result.MaxScore = &value, &maximum
		}
		attempt.Result.CompletedAt = time.Unix(0, completedAt).UTC()
		if err := attempt.Validate(); err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return attempts, nil
}

func qualificationOrder(qualification core.QualificationDescriptor) any {
	if qualification.LevelOrder == nil {
		return nil
	}
	return *qualification.LevelOrder
}

func qualificationScores(result core.QualificationResult) (any, any) {
	if result.Score == nil || result.MaxScore == nil {
		return nil, nil
	}
	return *result.Score, *result.MaxScore
}
