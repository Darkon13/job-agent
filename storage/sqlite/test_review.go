package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

var (
	_ storage.TestCatalogRepository = (*Store)(nil)
	_ storage.ReviewRepository      = (*Store)(nil)
)

func (store *Store) UpsertTestDefinition(ctx context.Context, definition core.TestDefinition) (bool, error) {
	if err := definition.Validate(); err != nil {
		return false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin test definition upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	familyID, familyName, levelID, levelName, levelOrder := qualificationColumns(definition.Qualification)
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO test_definitions
		(id, platform, external_id, title, family_id, family_name, level_id, level_name,
		level_order, last_attempt_fingerprint, observed_attempts, discovered_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		definition.ID, definition.Platform, definition.ExternalID, definition.Title,
		familyID, familyName, levelID, levelName, levelOrder, definition.LastAttemptFingerprint,
		definition.ObservedAttempts, definition.DiscoveredAt.UnixNano(), definition.UpdatedAt.UnixNano())
	if err != nil {
		return false, fmt.Errorf("insert test definition %s: %w", definition.ID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil {
		return false, err
	}
	if !created {
		var storedPlatform, storedExternalID, storedFamilyID, storedLevelID string
		var storedUpdatedAt int64
		if err := tx.QueryRowContext(ctx, `SELECT platform, external_id, family_id, level_id, updated_at
			FROM test_definitions WHERE id = ?`, definition.ID).
			Scan(&storedPlatform, &storedExternalID, &storedFamilyID, &storedLevelID, &storedUpdatedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, errors.New("test definition identity conflicts with another catalog entry")
			}
			return false, fmt.Errorf("load test definition identity: %w", err)
		}
		if storedPlatform != string(definition.Platform) || storedExternalID != definition.ExternalID || storedFamilyID != familyID || storedLevelID != levelID {
			return false, errors.New("test definition id conflicts with different platform metadata")
		}
		if definition.UpdatedAt.UnixNano() < storedUpdatedAt {
			return false, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE test_definitions SET
			title = ?, family_name = ?, level_name = ?, level_order = ?,
			last_attempt_fingerprint = CASE
				WHEN ? >= observed_attempts AND ? <> '' THEN ? ELSE last_attempt_fingerprint END,
			observed_attempts = MAX(observed_attempts, ?), updated_at = ?
			WHERE id = ?`, definition.Title, familyName, levelName, levelOrder,
			definition.ObservedAttempts, definition.LastAttemptFingerprint, definition.LastAttemptFingerprint,
			definition.ObservedAttempts, definition.UpdatedAt.UnixNano(), definition.ID); err != nil {
			return false, fmt.Errorf("update test definition %s: %w", definition.ID, err)
		}
	}

	for _, question := range definition.Questions {
		options, err := json.Marshal(question.Options)
		if err != nil {
			return false, fmt.Errorf("encode test question options: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO test_questions
			(test_definition_id, fingerprint, text, kind, options, first_seen_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(test_definition_id, fingerprint) DO UPDATE SET
			text = excluded.text, kind = excluded.kind, options = excluded.options,
			first_seen_at = MIN(first_seen_at, excluded.first_seen_at),
			last_seen_at = MAX(last_seen_at, excluded.last_seen_at)`,
			definition.ID, question.Fingerprint, question.Text, question.Kind, options,
			question.FirstSeenAt.UnixNano(), question.LastSeenAt.UnixNano()); err != nil {
			return false, fmt.Errorf("upsert test question %s: %w", question.Fingerprint, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit test definition %s: %w", definition.ID, err)
	}
	return created, nil
}

func (store *Store) TestDefinition(ctx context.Context, id core.TestDefinitionID) (core.TestDefinition, error) {
	if id == "" {
		return core.TestDefinition{}, errors.New("test definition id is required")
	}
	return loadTestDefinition(ctx, store.db, id)
}

func (store *Store) ListTestDefinitions(ctx context.Context, filter storage.TestDefinitionFilter) ([]core.TestDefinition, error) {
	query := `SELECT id FROM test_definitions WHERE 1 = 1`
	args := make([]any, 0, 3)
	if filter.Platform != "" {
		query += ` AND platform = ?`
		args = append(args, filter.Platform)
	}
	if filter.FamilyID != "" {
		query += ` AND family_id = ?`
		args = append(args, filter.FamilyID)
	}
	if filter.LevelID != "" {
		query += ` AND level_id = ?`
		args = append(args, filter.LevelID)
	}
	query += ` ORDER BY platform, family_name, level_order, level_name, title, id`
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list test definitions: %w", err)
	}
	var ids []core.TestDefinitionID
	for rows.Next() {
		var id core.TestDefinitionID
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan test definition id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close test definition rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate test definitions: %w", err)
	}

	definitions := make([]core.TestDefinition, 0, len(ids))
	for _, id := range ids {
		definition, err := store.TestDefinition(ctx, id)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func (store *Store) CreateReviewSession(ctx context.Context, session core.ReviewSession) (bool, error) {
	if err := session.Validate(); err != nil {
		return false, err
	}
	if session.Status != core.ReviewPending || session.Revision != 1 {
		return false, errors.New("review repository accepts only initialized pending sessions")
	}
	questionnaire, err := json.Marshal(session.Questionnaire)
	if err != nil {
		return false, fmt.Errorf("encode review session questionnaire: %w", err)
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO review_sessions
		(id, test_definition_id, platform, profile_id, correlation_id, status, revision, created_at, updated_at, questionnaire, answer_block_tag)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, session.ID, session.TestDefinitionID, session.Platform,
		session.ProfileID, session.CorrelationID, session.Status, session.Revision,
		session.CreatedAt.UnixNano(), session.UpdatedAt.UnixNano(), questionnaire, session.AnswerBlockTag)
	if err != nil {
		return false, fmt.Errorf("create review session %s: %w", session.ID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil || created {
		return created, err
	}
	stored, err := store.ReviewSession(ctx, session.ID)
	if err != nil {
		return false, err
	}
	if stored.TestDefinitionID != session.TestDefinitionID || stored.ProfileID != session.ProfileID || stored.CorrelationID != session.CorrelationID {
		return false, errors.New("review session id conflicts with a different session")
	}
	return false, nil
}

const reviewSessionColumns = `id, test_definition_id, platform, profile_id, correlation_id,
	status, revision, created_at, updated_at, questionnaire, answer_block_tag`

func (store *Store) ReviewSession(ctx context.Context, id core.ReviewSessionID) (core.ReviewSession, error) {
	if id == "" {
		return core.ReviewSession{}, errors.New("review session id is required")
	}
	return scanReviewSession(store.db.QueryRowContext(ctx,
		`SELECT `+reviewSessionColumns+` FROM review_sessions WHERE id = ?`, id))
}

func (store *Store) ListReviewSessions(ctx context.Context, filter storage.ReviewSessionFilter) ([]core.ReviewSession, error) {
	query := `SELECT ` + reviewSessionColumns + `
		FROM review_sessions
		WHERE (? = '' OR status = ?)
		  AND (? = '' OR profile_id = ?)
		  AND (? = '' OR platform = ?)
		ORDER BY updated_at DESC, id`
	args := []any{filter.Status, filter.Status, filter.ProfileID, filter.ProfileID, filter.Platform, filter.Platform}
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list review sessions: %w", err)
	}
	defer rows.Close()
	sessions := make([]core.ReviewSession, 0)
	for rows.Next() {
		session, err := scanReviewSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate review sessions: %w", err)
	}
	return sessions, nil
}

func scanReviewSession(row rowScanner) (core.ReviewSession, error) {
	var session core.ReviewSession
	var createdAt, updatedAt int64
	var questionnaire []byte
	if err := row.Scan(&session.ID, &session.TestDefinitionID, &session.Platform, &session.ProfileID,
		&session.CorrelationID, &session.Status, &session.Revision, &createdAt, &updatedAt, &questionnaire,
		&session.AnswerBlockTag); err != nil {
		return core.ReviewSession{}, err
	}
	if len(bytes.TrimSpace(questionnaire)) != 0 && string(bytes.TrimSpace(questionnaire)) != "{}" {
		if err := json.Unmarshal(questionnaire, &session.Questionnaire); err != nil {
			return core.ReviewSession{}, fmt.Errorf("decode review session questionnaire: %w", err)
		}
	}
	session.CreatedAt = time.Unix(0, createdAt).UTC()
	session.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return session, session.Validate()
}

func (store *Store) ReviewPrompt(ctx context.Context, id core.ReviewPromptID) (core.ReviewPrompt, error) {
	if id == "" {
		return core.ReviewPrompt{}, errors.New("review prompt id is required")
	}
	row := store.db.QueryRowContext(ctx, `SELECT session_id, revision, question, deadline, created_at
		FROM review_prompts WHERE id = ?`, id)
	var prompt core.ReviewPrompt
	var question []byte
	var deadline sql.NullInt64
	var createdAt int64
	prompt.ID = id
	if err := row.Scan(&prompt.SessionID, &prompt.Revision, &question, &deadline, &createdAt); err != nil {
		return core.ReviewPrompt{}, err
	}
	if err := json.Unmarshal(question, &prompt.Question); err != nil {
		return core.ReviewPrompt{}, fmt.Errorf("decode review prompt question: %w", err)
	}
	prompt.Deadline = timeFromNull(deadline)
	prompt.CreatedAt = time.Unix(0, createdAt).UTC()
	return prompt, prompt.Validate()
}

func (store *Store) SaveReviewPrompt(ctx context.Context, session core.ReviewSession, prompt core.ReviewPrompt, expectedRevision uint64) error {
	if err := session.Validate(); err != nil {
		return err
	}
	if err := prompt.Validate(); err != nil {
		return err
	}
	if session.Status != core.ReviewWaiting || session.Revision != expectedRevision || prompt.SessionID != session.ID || prompt.Revision != expectedRevision {
		return errors.New("review prompt does not match waiting session revision")
	}
	question, err := json.Marshal(prompt.Question)
	if err != nil {
		return fmt.Errorf("encode review prompt question: %w", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin review prompt save: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE review_sessions SET status = ?, updated_at = ?
		WHERE id = ? AND revision = ? AND status IN (?, ?)`, session.Status, session.UpdatedAt.UnixNano(),
		session.ID, expectedRevision, core.ReviewPending, core.ReviewAnswered)
	if err != nil {
		return fmt.Errorf("mark review session waiting: %w", err)
	}
	updated, err := oneRowAffected(result)
	if err != nil {
		return err
	}
	if !updated {
		return storage.ErrRevisionConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO review_prompts
		(id, session_id, revision, question, deadline, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		prompt.ID, prompt.SessionID, prompt.Revision, question, nullableTime(prompt.Deadline), prompt.CreatedAt.UnixNano()); err != nil {
		return fmt.Errorf("insert review prompt %s: %w", prompt.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit review prompt %s: %w", prompt.ID, err)
	}
	return nil
}

func (store *Store) AppendReviewSelection(ctx context.Context, session core.ReviewSession, selection core.ReviewSelection, expectedRevision uint64) error {
	if err := session.Validate(); err != nil {
		return err
	}
	if err := selection.Validate(); err != nil {
		return err
	}
	if session.Status != core.ReviewAnswered || session.Revision != expectedRevision+1 || selection.SessionID != session.ID || selection.Revision != expectedRevision {
		return errors.New("review selection does not match answered session revision")
	}
	selectedOptions, err := json.Marshal(selection.SelectedOptions)
	if err != nil {
		return fmt.Errorf("encode review selected options: %w", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin review selection append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE review_sessions SET status = ?, revision = ?, updated_at = ?
		WHERE id = ? AND revision = ? AND status = ?
		AND EXISTS (SELECT 1 FROM review_prompts
			WHERE id = ? AND session_id = ? AND revision = ?)`, session.Status, session.Revision,
		session.UpdatedAt.UnixNano(), session.ID, expectedRevision, core.ReviewWaiting,
		selection.PromptID, session.ID, expectedRevision)
	if err != nil {
		return fmt.Errorf("advance review session revision: %w", err)
	}
	updated, err := oneRowAffected(result)
	if err != nil {
		return err
	}
	if !updated {
		return storage.ErrRevisionConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO review_selections
		(session_id, revision, prompt_id, selected_options, text, source, assessment, selected_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, selection.SessionID, selection.Revision, selection.PromptID,
		selectedOptions, selection.Text, selection.Source, selection.Assessment, selection.SelectedAt.UnixNano()); err != nil {
		return fmt.Errorf("insert review selection: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit review selection: %w", err)
	}
	return nil
}

func (store *Store) ReviewSelections(ctx context.Context, sessionID core.ReviewSessionID) ([]core.ReviewSelection, error) {
	if sessionID == "" {
		return nil, errors.New("review session id is required")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT prompt_id, revision, selected_options, text, source,
		assessment, selected_at FROM review_selections WHERE session_id = ? ORDER BY revision`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list review selections: %w", err)
	}
	defer rows.Close()
	var selections []core.ReviewSelection
	for rows.Next() {
		selection := core.ReviewSelection{SessionID: sessionID}
		var selectedOptions []byte
		var selectedAt int64
		if err := rows.Scan(&selection.PromptID, &selection.Revision, &selectedOptions, &selection.Text,
			&selection.Source, &selection.Assessment, &selectedAt); err != nil {
			return nil, fmt.Errorf("scan review selection: %w", err)
		}
		if err := json.Unmarshal(selectedOptions, &selection.SelectedOptions); err != nil {
			return nil, fmt.Errorf("decode review selected options: %w", err)
		}
		selection.SelectedAt = time.Unix(0, selectedAt).UTC()
		if err := selection.Validate(); err != nil {
			return nil, err
		}
		selections = append(selections, selection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate review selections: %w", err)
	}
	return selections, nil
}

type definitionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadTestDefinition(ctx context.Context, querier definitionQuerier, id core.TestDefinitionID) (core.TestDefinition, error) {
	row := querier.QueryRowContext(ctx, `SELECT platform, external_id, title, family_id, family_name,
		level_id, level_name, level_order, last_attempt_fingerprint, observed_attempts, discovered_at, updated_at
		FROM test_definitions WHERE id = ?`, id)
	var definition core.TestDefinition
	var familyID, familyName, levelID, levelName string
	var levelOrder sql.NullInt64
	var discoveredAt, updatedAt int64
	definition.ID = id
	if err := row.Scan(&definition.Platform, &definition.ExternalID, &definition.Title, &familyID, &familyName,
		&levelID, &levelName, &levelOrder, &definition.LastAttemptFingerprint, &definition.ObservedAttempts,
		&discoveredAt, &updatedAt); err != nil {
		return core.TestDefinition{}, err
	}
	definition.DiscoveredAt = time.Unix(0, discoveredAt).UTC()
	definition.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if familyID != "" || levelID != "" {
		qualification := core.QualificationDescriptor{
			FamilyID: familyID, FamilyName: familyName, LevelID: levelID, LevelName: levelName,
		}
		if levelOrder.Valid {
			order := int(levelOrder.Int64)
			qualification.LevelOrder = &order
		}
		definition.Qualification = &qualification
	}
	rows, err := querier.QueryContext(ctx, `SELECT fingerprint, text, kind, options, first_seen_at, last_seen_at
		FROM test_questions WHERE test_definition_id = ? ORDER BY fingerprint`, id)
	if err != nil {
		return core.TestDefinition{}, fmt.Errorf("load test questions: %w", err)
	}
	defer rows.Close()
	definition.Questions = []core.TestQuestion{}
	for rows.Next() {
		var question core.TestQuestion
		var options []byte
		var firstSeenAt, lastSeenAt int64
		if err := rows.Scan(&question.Fingerprint, &question.Text, &question.Kind, &options, &firstSeenAt, &lastSeenAt); err != nil {
			return core.TestDefinition{}, fmt.Errorf("scan test question: %w", err)
		}
		if err := json.Unmarshal(options, &question.Options); err != nil {
			return core.TestDefinition{}, fmt.Errorf("decode test question options: %w", err)
		}
		question.FirstSeenAt = time.Unix(0, firstSeenAt).UTC()
		question.LastSeenAt = time.Unix(0, lastSeenAt).UTC()
		definition.Questions = append(definition.Questions, question)
	}
	if err := rows.Err(); err != nil {
		return core.TestDefinition{}, fmt.Errorf("iterate test questions: %w", err)
	}
	return definition, definition.Validate()
}

func qualificationColumns(qualification *core.QualificationDescriptor) (string, string, string, string, any) {
	if qualification == nil {
		return "", "", "", "", nil
	}
	var order any
	if qualification.LevelOrder != nil {
		order = *qualification.LevelOrder
	}
	return qualification.FamilyID, qualification.FamilyName, qualification.LevelID, qualification.LevelName, order
}
