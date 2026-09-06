package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	_ "modernc.org/sqlite"
)

var (
	_ storage.VacancyRepository             = (*Store)(nil)
	_ storage.SearchRunRepository           = (*Store)(nil)
	_ storage.ApplicationCampaignRepository = (*Store)(nil)
	_ storage.ApplicationRepository         = (*Store)(nil)
	_ storage.ApplicationBudgetRepository   = (*Store)(nil)
	_ storage.ConversationRepository        = (*Store)(nil)
	_ broker.TaskQueue                      = (*Store)(nil)
	_ broker.TaskStore                      = (*Store)(nil)
)

var ErrSchemaNotReady = errors.New("sqlite schema is not ready; run job-agent-migrate up")

type Store struct {
	db *sql.DB
}

type Stats struct {
	SearchRuns           int
	Vacancies            int
	Discoveries          int
	Applications         int
	ApplicationCampaigns int
	CampaignApplications int
	Tasks                int
	TestDefinitions      int
	ReviewSessions       int
	ReviewSelections     int
	Conversations        int
	Messages             int
	FollowUps            int
}

type TaskCount struct {
	Type   core.TaskType
	Status core.TaskStatus
	Count  int
}

type ApplicationCount struct {
	Status       core.ApplicationStatus
	DecisionCode string
	Count        int
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite path is required")
	}
	path, err := prepareDatabasePath(path)
	if err != nil {
		return nil, err
	}
	dsn, err := dataSourceName(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	store := &Store{db: db}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := store.checkSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *Store) checkSchema(ctx context.Context) error {
	var version uint
	var dirty bool
	if err := store.db.QueryRowContext(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaNotReady, err)
	}
	if dirty {
		return fmt.Errorf("%w: migration version %d is dirty", ErrSchemaNotReady, version)
	}
	if version != LatestSchemaVersion {
		return fmt.Errorf("%w: database version %d, required %d", ErrSchemaNotReady, version, LatestSchemaVersion)
	}
	return nil
}

func (store *Store) UpsertVacancy(ctx context.Context, vacancy core.Vacancy) (bool, error) {
	if err := vacancy.Validate(); err != nil {
		return false, err
	}
	attributes, err := marshalAttributes(vacancy.Attributes)
	if err != nil {
		return false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin vacancy upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO vacancies
		(platform, external_id, url, title, employer, state, published_at, observed_at, attributes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		vacancy.Platform, vacancy.ExternalID, vacancy.URL, vacancy.Title, vacancy.Employer, vacancy.State,
		nullableTime(vacancy.PublishedAt), vacancy.ObservedAt.UnixNano(), attributes)
	if err != nil {
		return false, fmt.Errorf("insert vacancy %s: %w", vacancy.Key(), err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read vacancy insert result: %w", err)
	}
	created := affected == 1
	if !created {
		if _, err := tx.ExecContext(ctx, `UPDATE vacancies SET
			url = ?, title = ?, employer = ?, state = ?, published_at = ?, observed_at = ?, attributes = ?
			WHERE platform = ? AND external_id = ? AND observed_at <= ?`,
			vacancy.URL, vacancy.Title, vacancy.Employer, vacancy.State, nullableTime(vacancy.PublishedAt),
			vacancy.ObservedAt.UnixNano(), attributes, vacancy.Platform, vacancy.ExternalID, vacancy.ObservedAt.UnixNano()); err != nil {
			return false, fmt.Errorf("update vacancy %s: %w", vacancy.Key(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit vacancy %s: %w", vacancy.Key(), err)
	}
	return created, nil
}

func (store *Store) RecordDiscovery(ctx context.Context, discovery core.VacancyDiscovery) (bool, error) {
	if err := discovery.Validate(); err != nil {
		return false, err
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO vacancy_discoveries
		(platform, external_id, search_id, profile_id, discovered_at) VALUES (?, ?, ?, ?, ?)`,
		discovery.VacancyKey.Platform, discovery.VacancyKey.ExternalID, discovery.SearchID,
		discovery.ProfileID, discovery.DiscoveredAt.UnixNano())
	if err != nil {
		return false, fmt.Errorf("insert vacancy discovery %s: %w", discovery.VacancyKey, err)
	}
	return oneRowAffected(result)
}

func (store *Store) CreateApplication(ctx context.Context, candidate core.Application) (core.Application, bool, error) {
	if candidate.ID == "" || candidate.Status != core.ApplicationNew || candidate.CreatedAt.IsZero() || candidate.UpdatedAt.IsZero() {
		return core.Application{}, false, errors.New("application repository accepts only initialized new applications")
	}
	if err := candidate.Key.Validate(); err != nil {
		return core.Application{}, false, err
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO applications
		(id, profile_id, platform, external_id, status, attempts, external_negotiation_id,
		 failure_category, failure_message, decision_code, decision_reason, prepared_resume_id,
		 prepared_message, created_at, updated_at, prepared_at, submitted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidate.ID, candidate.Key.ProfileID, candidate.Key.Vacancy.Platform, candidate.Key.Vacancy.ExternalID,
		candidate.Status, candidate.Attempts, candidate.ExternalNegotiationID, candidate.FailureCategory,
		candidate.FailureMessage, candidate.DecisionCode, candidate.DecisionReason, candidate.PreparedResumeID, candidate.PreparedMessage,
		candidate.CreatedAt.UnixNano(), candidate.UpdatedAt.UnixNano(), nullableTime(candidate.PreparedAt), nullableTime(candidate.SubmittedAt))
	if err != nil {
		return core.Application{}, false, fmt.Errorf("insert application: %w", err)
	}
	created, err := oneRowAffected(result)
	if err != nil {
		return core.Application{}, false, err
	}
	stored, err := store.Application(ctx, candidate.Key)
	if err != nil {
		return core.Application{}, false, fmt.Errorf("load stored application: %w", err)
	}
	return stored, created, nil
}

func (store *Store) Enqueue(ctx context.Context, task core.Task) (bool, error) {
	if err := validateNewTask(task); err != nil {
		return false, err
	}
	var failureCategory, failureMessage any
	if task.Failure != nil {
		failureCategory, failureMessage = task.Failure.Category, task.Failure.Message
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO tasks
		(id, type, status, idempotency_key, source, platform, profile_id, correlation_id, payload,
		 attempts, available_at, deadline, created_at, updated_at, failure_category, failure_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.Type, task.Status, task.IdempotencyKey, task.Source, task.Platform, task.ProfileID,
		task.CorrelationID, []byte(task.Payload), task.Attempts, task.AvailableAt.UnixNano(), nullableTime(task.Deadline),
		task.CreatedAt.UnixNano(), task.UpdatedAt.UnixNano(), failureCategory, failureMessage)
	if err != nil {
		return false, fmt.Errorf("enqueue task: %w", err)
	}
	created, err := oneRowAffected(result)
	if err != nil || created {
		return created, err
	}
	existing, err := store.TaskByIdempotencyKey(ctx, task.IdempotencyKey)
	if err != nil {
		return false, fmt.Errorf("load existing task: %w", err)
	}
	if existing.Type != task.Type || existing.Platform != task.Platform || existing.ProfileID != task.ProfileID || !bytes.Equal(existing.Payload, task.Payload) {
		return false, errors.New("task idempotency key conflicts with a different command")
	}
	return false, nil
}

func (store *Store) Application(ctx context.Context, key core.ApplicationKey) (core.Application, error) {
	if err := key.Validate(); err != nil {
		return core.Application{}, err
	}
	row := store.db.QueryRowContext(ctx, `SELECT id, status, attempts, external_negotiation_id,
		failure_category, failure_message, decision_code, decision_reason, prepared_resume_id, prepared_message,
		created_at, updated_at, prepared_at, submitted_at
		FROM applications WHERE profile_id = ? AND platform = ? AND external_id = ?`,
		key.ProfileID, key.Vacancy.Platform, key.Vacancy.ExternalID)
	var application core.Application
	var createdAt, updatedAt int64
	var preparedAt, submittedAt sql.NullInt64
	application.Key = key
	if err := row.Scan(&application.ID, &application.Status, &application.Attempts, &application.ExternalNegotiationID,
		&application.FailureCategory, &application.FailureMessage, &application.DecisionCode,
		&application.DecisionReason, &application.PreparedResumeID, &application.PreparedMessage, &createdAt, &updatedAt, &preparedAt, &submittedAt); err != nil {
		return core.Application{}, err
	}
	application.CreatedAt = time.Unix(0, createdAt).UTC()
	application.UpdatedAt = time.Unix(0, updatedAt).UTC()
	application.PreparedAt = timeFromNull(preparedAt)
	application.SubmittedAt = timeFromNull(submittedAt)
	return application, nil
}

func (store *Store) SaveApplication(ctx context.Context, candidate core.Application, expectedStatus core.ApplicationStatus) error {
	if candidate.ID == "" || expectedStatus == "" {
		return errors.New("application save requires id and expected status")
	}
	if err := candidate.Key.Validate(); err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `UPDATE applications SET
		status = ?, attempts = ?, external_negotiation_id = ?, failure_category = ?,
		failure_message = ?, decision_code = ?, decision_reason = ?, prepared_resume_id = ?, prepared_message = ?,
		updated_at = ?, prepared_at = ?, submitted_at = ?
		WHERE id = ? AND profile_id = ? AND platform = ? AND external_id = ? AND status = ?`,
		candidate.Status, candidate.Attempts, candidate.ExternalNegotiationID, candidate.FailureCategory,
		candidate.FailureMessage, candidate.DecisionCode, candidate.DecisionReason, candidate.PreparedResumeID, candidate.PreparedMessage,
		candidate.UpdatedAt.UnixNano(), nullableTime(candidate.PreparedAt), nullableTime(candidate.SubmittedAt),
		candidate.ID, candidate.Key.ProfileID, candidate.Key.Vacancy.Platform, candidate.Key.Vacancy.ExternalID, expectedStatus)
	if err != nil {
		return fmt.Errorf("save application %s: %w", candidate.ID, err)
	}
	updated, err := oneRowAffected(result)
	if err != nil {
		return err
	}
	if !updated {
		return storage.ErrRevisionConflict
	}
	return nil
}

func (store *Store) Vacancy(ctx context.Context, key core.VacancyKey) (core.Vacancy, error) {
	if err := key.Validate(); err != nil {
		return core.Vacancy{}, err
	}
	row := store.db.QueryRowContext(ctx, `SELECT url, title, employer, state, published_at, observed_at, attributes
		FROM vacancies WHERE platform = ? AND external_id = ?`, key.Platform, key.ExternalID)
	var vacancy core.Vacancy
	var publishedAt sql.NullInt64
	var observedAt int64
	var attributes []byte
	vacancy.Platform = key.Platform
	vacancy.ExternalID = key.ExternalID
	if err := row.Scan(&vacancy.URL, &vacancy.Title, &vacancy.Employer, &vacancy.State, &publishedAt, &observedAt, &attributes); err != nil {
		return core.Vacancy{}, err
	}
	vacancy.PublishedAt = timeFromNull(publishedAt)
	vacancy.ObservedAt = time.Unix(0, observedAt).UTC()
	if len(attributes) != 0 {
		if err := json.Unmarshal(attributes, &vacancy.Attributes); err != nil {
			return core.Vacancy{}, fmt.Errorf("decode vacancy attributes: %w", err)
		}
	}
	return vacancy, nil
}

func (store *Store) TaskByIdempotencyKey(ctx context.Context, key string) (core.Task, error) {
	if key == "" {
		return core.Task{}, errors.New("task idempotency key is required")
	}
	row := store.db.QueryRowContext(ctx, `SELECT id, type, status, idempotency_key, source, platform,
		profile_id, correlation_id, payload, attempts, available_at, deadline, created_at, updated_at,
		failure_category, failure_message FROM tasks WHERE idempotency_key = ?`, key)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Task{}, broker.ErrTaskNotFound
	}
	return task, err
}

func (store *Store) Stats(ctx context.Context) (Stats, error) {
	var stats Stats
	for _, item := range []struct {
		table string
		value *int
	}{
		{"vacancies", &stats.Vacancies}, {"vacancy_discoveries", &stats.Discoveries},
		{"search_runs", &stats.SearchRuns},
		{"applications", &stats.Applications}, {"tasks", &stats.Tasks},
		{"application_campaigns", &stats.ApplicationCampaigns},
		{"application_campaign_items", &stats.CampaignApplications},
		{"test_definitions", &stats.TestDefinitions}, {"review_sessions", &stats.ReviewSessions},
		{"review_selections", &stats.ReviewSelections},
		{"conversations", &stats.Conversations}, {"conversation_messages", &stats.Messages},
		{"conversation_follow_ups", &stats.FollowUps},
	} {
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+item.table).Scan(item.value); err != nil {
			return Stats{}, fmt.Errorf("count %s: %w", item.table, err)
		}
	}
	return stats, nil
}

// TaskCounts exposes an operator-safe queue summary without task payloads,
// profile identifiers or external vacancy identifiers.
func (store *Store) TaskCounts(ctx context.Context) ([]TaskCount, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT type, status, COUNT(*)
		FROM tasks GROUP BY type, status ORDER BY type, status`)
	if err != nil {
		return nil, fmt.Errorf("count tasks by type and status: %w", err)
	}
	defer rows.Close()
	counts := make([]TaskCount, 0)
	for rows.Next() {
		var item TaskCount
		if err := rows.Scan(&item.Type, &item.Status, &item.Count); err != nil {
			return nil, fmt.Errorf("scan task count: %w", err)
		}
		counts = append(counts, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task counts: %w", err)
	}
	return counts, nil
}

// ApplicationCounts exposes lifecycle totals without profile, vacancy,
// decision text or prepared message data.
func (store *Store) ApplicationCounts(ctx context.Context) ([]ApplicationCount, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT status, decision_code, COUNT(*)
		FROM applications GROUP BY status, decision_code ORDER BY status, decision_code`)
	if err != nil {
		return nil, fmt.Errorf("count applications by status: %w", err)
	}
	defer rows.Close()
	counts := make([]ApplicationCount, 0)
	for rows.Next() {
		var item ApplicationCount
		if err := rows.Scan(&item.Status, &item.DecisionCode, &item.Count); err != nil {
			return nil, fmt.Errorf("scan application count: %w", err)
		}
		counts = append(counts, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate application counts: %w", err)
	}
	return counts, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(row rowScanner) (core.Task, error) {
	var task core.Task
	var payload []byte
	var availableAt, createdAt, updatedAt int64
	var deadline sql.NullInt64
	var failureCategory, failureMessage sql.NullString
	if err := row.Scan(&task.ID, &task.Type, &task.Status, &task.IdempotencyKey, &task.Source, &task.Platform,
		&task.ProfileID, &task.CorrelationID, &payload, &task.Attempts, &availableAt, &deadline,
		&createdAt, &updatedAt, &failureCategory, &failureMessage); err != nil {
		return core.Task{}, err
	}
	task.Payload = append(json.RawMessage(nil), payload...)
	task.AvailableAt = time.Unix(0, availableAt).UTC()
	task.Deadline = timeFromNull(deadline)
	task.CreatedAt = time.Unix(0, createdAt).UTC()
	task.UpdatedAt = time.Unix(0, updatedAt).UTC()
	if failureCategory.Valid {
		task.Failure = &core.TaskFailure{Category: core.ErrorCategory(failureCategory.String), Message: failureMessage.String}
	}
	return task, nil
}

func validateNewTask(task core.Task) error {
	if task.ID == "" || task.Type == "" || task.IdempotencyKey == "" || task.CorrelationID == "" {
		return errors.New("queued task requires id, type, idempotency key and correlation id")
	}
	if task.Status != core.TaskNew || len(task.Payload) == 0 || !json.Valid(task.Payload) {
		return errors.New("queue accepts only initialized new tasks with valid JSON payload")
	}
	return nil
}

func dataSourceName(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve sqlite path: %w", err)
	}
	value := url.URL{Scheme: "file", Path: absolute}
	query := value.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	value.RawQuery = query.Encode()
	return value.String(), nil
}

func prepareDatabasePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve sqlite path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return "", fmt.Errorf("create sqlite directory: %w", err)
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fmt.Errorf("create sqlite database: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close prepared sqlite database: %w", err)
	}
	return absolute, nil
}

func marshalAttributes(attributes map[string]any) ([]byte, error) {
	if attributes == nil {
		return nil, nil
	}
	value, err := json.Marshal(attributes)
	if err != nil {
		return nil, fmt.Errorf("encode vacancy attributes: %w", err)
	}
	return value, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixNano()
}

func timeFromNull(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := time.Unix(0, value.Int64).UTC()
	return &result
}

func oneRowAffected(result sql.Result) (bool, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read affected rows: %w", err)
	}
	return affected == 1, nil
}
