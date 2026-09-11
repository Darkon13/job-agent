package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var _ storage.AnswerBlockRevisionRepository = (*Store)(nil)

func (store *Store) AppendAnswerBlockRevision(ctx context.Context, candidate core.AnswerBlockRevision) (core.AnswerBlockRevision, error) {
	if strings.TrimSpace(candidate.BlockTag) == "" || strings.TrimSpace(candidate.Source) == "" || candidate.CreatedAt.IsZero() {
		return core.AnswerBlockRevision{}, errors.New("answer block revision requires tag, source and created_at")
	}
	if err := core.ValidateAnswerBlock(candidate.Block()); err != nil {
		return core.AnswerBlockRevision{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.AnswerBlockRevision{}, fmt.Errorf("begin answer block revision append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var latest uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) FROM answer_block_revisions WHERE block_tag = ?`, candidate.BlockTag).Scan(&latest); err != nil {
		return core.AnswerBlockRevision{}, fmt.Errorf("load latest answer block revision: %w", err)
	}
	revision := latest + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO answer_block_revisions
		(block_tag, revision, name, kind, platform, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		candidate.BlockTag, revision, candidate.Name, candidate.Kind, candidate.Platform,
		candidate.Source, candidate.CreatedAt.UnixNano()); err != nil {
		return core.AnswerBlockRevision{}, fmt.Errorf("insert answer block revision %s: %w", candidate.BlockTag, err)
	}
	for position, answer := range candidate.Answers {
		selected, err := json.Marshal(answer.SelectedOptions)
		if err != nil {
			return core.AnswerBlockRevision{}, fmt.Errorf("encode selected options: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO answer_block_revision_answers
			(block_tag, revision, question, question_fingerprint, selected_options, text, position)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			candidate.BlockTag, revision, answer.Question, answer.QuestionFingerprint,
			selected, answer.Text, position); err != nil {
			return core.AnswerBlockRevision{}, fmt.Errorf("insert answer block revision answer: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return core.AnswerBlockRevision{}, fmt.Errorf("commit answer block revision: %w", err)
	}
	stored := candidate
	stored.Revision = revision
	return stored, nil
}

func (store *Store) LatestAnswerBlockRevision(ctx context.Context, tag string) (core.AnswerBlockRevision, bool, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return core.AnswerBlockRevision{}, false, errors.New("answer block revision tag is required")
	}
	var revision core.AnswerBlockRevision
	var createdAt int64
	err := store.db.QueryRowContext(ctx, `SELECT revision, name, kind, platform, source, created_at
		FROM answer_block_revisions WHERE block_tag = ? ORDER BY revision DESC LIMIT 1`, tag).
		Scan(&revision.Revision, &revision.Name, &revision.Kind, &revision.Platform, &revision.Source, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.AnswerBlockRevision{}, false, nil
		}
		return core.AnswerBlockRevision{}, false, fmt.Errorf("load latest answer block revision %s: %w", tag, err)
	}
	revision.BlockTag = tag
	revision.CreatedAt = time.Unix(0, createdAt).UTC()
	rows, err := store.db.QueryContext(ctx, `SELECT question, question_fingerprint, selected_options, text
		FROM answer_block_revision_answers WHERE block_tag = ? AND revision = ? ORDER BY position`, tag, revision.Revision)
	if err != nil {
		return core.AnswerBlockRevision{}, false, fmt.Errorf("load answer block revision answers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var answer core.StoredAnswer
		var selected []byte
		if err := rows.Scan(&answer.Question, &answer.QuestionFingerprint, &selected, &answer.Text); err != nil {
			return core.AnswerBlockRevision{}, false, err
		}
		if len(selected) != 0 {
			if err := json.Unmarshal(selected, &answer.SelectedOptions); err != nil {
				return core.AnswerBlockRevision{}, false, fmt.Errorf("decode selected options: %w", err)
			}
		}
		revision.Answers = append(revision.Answers, answer)
	}
	if err := rows.Err(); err != nil {
		return core.AnswerBlockRevision{}, false, err
	}
	if err := revision.Validate(); err != nil {
		return core.AnswerBlockRevision{}, false, err
	}
	return revision, true, nil
}
