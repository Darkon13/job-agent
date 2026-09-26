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

func (store *Store) CreateConversation(ctx context.Context, candidate core.Conversation) (core.Conversation, bool, error) {
	if err := candidate.Validate(); err != nil {
		return core.Conversation{}, false, err
	}
	if candidate.Status != core.ConversationActive || candidate.Revision != 1 {
		return core.Conversation{}, false, errors.New("conversation repository accepts only initialized active conversations")
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO conversations
		(id, platform, profile_id, external_id, application_id, vacancy_title, employer, vacancy_url, unread_count, status, last_message_id,
			 last_message_at, last_incoming_at, last_outgoing_at, last_read_at, created_at, updated_at, revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, candidate.ID, candidate.Platform,
		candidate.ProfileID, candidate.ExternalID, candidate.ApplicationID, candidate.VacancyTitle, candidate.Employer, candidate.VacancyURL, candidate.UnreadCount,
		candidate.Status,
		candidate.LastMessageID, nullableTime(candidate.LastMessageAt), nullableTime(candidate.LastIncomingAt),
		nullableTime(candidate.LastOutgoingAt), nullableTime(candidate.LastReadAt), candidate.CreatedAt.UnixNano(), candidate.UpdatedAt.UnixNano(), candidate.Revision)
	if err != nil {
		return core.Conversation{}, false, fmt.Errorf("create conversation %s: %w", candidate.ID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil {
		return core.Conversation{}, false, err
	}
	if created {
		return candidate, true, nil
	}
	stored, err := store.Conversation(ctx, candidate.ID)
	if errors.Is(err, sql.ErrNoRows) {
		stored, err = store.conversationByExternal(ctx, candidate.Platform, candidate.ProfileID, candidate.ExternalID)
	}
	if err != nil {
		return core.Conversation{}, false, err
	}
	if stored.ID == candidate.ID && !sameConversationIdentity(stored, candidate) {
		return core.Conversation{}, false, errors.New("conversation id conflicts with a different conversation")
	}
	if stored.ID != candidate.ID && candidate.ApplicationID != "" && stored.ApplicationID != candidate.ApplicationID {
		return core.Conversation{}, false, errors.New("conversation external id conflicts with a different application")
	}
	return stored, false, nil
}

func (store *Store) Conversation(ctx context.Context, id core.ConversationID) (core.Conversation, error) {
	if id == "" {
		return core.Conversation{}, errors.New("conversation id is required")
	}
	return scanConversation(store.db.QueryRowContext(ctx, conversationSelect+` WHERE id = ?`, id))
}

func (store *Store) SaveConversation(ctx context.Context, candidate core.Conversation, expectedRevision uint64) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Revision != expectedRevision+1 {
		return errors.New("conversation candidate must advance revision exactly once")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE conversations SET status = ?, vacancy_title = ?, employer = ?, vacancy_url = ?, unread_count = ?, last_read_at = ?, updated_at = ?, revision = ?
		WHERE id = ? AND platform = ? AND profile_id = ? AND external_id = ? AND application_id = ?
		AND last_message_id = ? AND last_message_at IS ? AND last_incoming_at IS ? AND last_outgoing_at IS ?
		AND created_at = ? AND revision = ?`, candidate.Status, candidate.VacancyTitle, candidate.Employer, candidate.VacancyURL, candidate.UnreadCount, nullableTime(candidate.LastReadAt), candidate.UpdatedAt.UnixNano(), candidate.Revision,
		candidate.ID, candidate.Platform, candidate.ProfileID, candidate.ExternalID, candidate.ApplicationID,
		candidate.LastMessageID, nullableTime(candidate.LastMessageAt), nullableTime(candidate.LastIncomingAt), nullableTime(candidate.LastOutgoingAt),
		candidate.CreatedAt.UnixNano(), expectedRevision)
	if err != nil {
		return fmt.Errorf("save conversation %s: %w", candidate.ID, err)
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

func (store *Store) conversationByExternal(ctx context.Context, platform core.Platform, profileID core.ProfileID, externalID string) (core.Conversation, error) {
	return scanConversation(store.db.QueryRowContext(ctx, conversationSelect+
		` WHERE platform = ? AND profile_id = ? AND external_id = ?`, platform, profileID, externalID))
}

func (store *Store) ListConversations(ctx context.Context, filter storage.ConversationFilter) ([]core.Conversation, error) {
	query, args := conversationFilterQuery(filter)
	query += ` ORDER BY updated_at DESC, id`
	if filter.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, filter.Limit, max(filter.Offset, 0))
	}
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	result := make([]core.Conversation, 0)
	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		result = append(result, conversation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversations: %w", err)
	}
	return result, nil
}

// AttachConversationApplication links a chat to its application once. The
// stored updated_at stays untouched: the platform observation time must keep
// looking fresh to the sync path.
func (store *Store) AttachConversationApplication(ctx context.Context, id core.ConversationID, applicationID core.ApplicationID) (bool, error) {
	if id == "" || applicationID == "" {
		return false, errors.New("conversation attach requires conversation and application")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE conversations
		SET application_id = ?, revision = revision + 1
		WHERE id = ? AND application_id = ''`, applicationID, id)
	if err != nil {
		return false, fmt.Errorf("attach conversation application: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count attached conversations: %w", err)
	}
	return affected > 0, nil
}

// ApplicationConversations lists the chats linked to one application.
func (store *Store) ApplicationConversations(ctx context.Context, applicationID core.ApplicationID) ([]core.Conversation, error) {
	if applicationID == "" {
		return nil, errors.New("application conversations require application")
	}
	rows, err := store.db.QueryContext(ctx, conversationSelect+` WHERE application_id = ? ORDER BY id`, applicationID)
	if err != nil {
		return nil, fmt.Errorf("list application conversations: %w", err)
	}
	defer rows.Close()
	result := make([]core.Conversation, 0)
	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan application conversation: %w", err)
		}
		result = append(result, conversation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate application conversations: %w", err)
	}
	return result, nil
}

// MarkConversationsReadLocally marks every unread chat read. An empty profile
// sweeps every account (the dashboard "all accounts" view).
func (store *Store) MarkConversationsReadLocally(ctx context.Context, profileID core.ProfileID, now time.Time) (int, error) {
	if now.IsZero() {
		return 0, errors.New("conversation read sweep requires time")
	}
	query := `UPDATE conversations
		SET unread_count = 0, last_read_at = ?, updated_at = ?, revision = revision + 1
		WHERE unread_count > 0`
	args := []any{now.UnixNano(), now.UnixNano()}
	if profileID != "" {
		query += ` AND profile_id = ?`
		args = append(args, profileID)
	}
	result, err := store.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("mark conversations read: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count read conversations: %w", err)
	}
	return int(affected), nil
}

// PurgeOrphanConversations deletes conversations of the profile whose
// application is no longer present (removed by retention or manually).
func (store *Store) PurgeOrphanConversations(ctx context.Context, profileID core.ProfileID) (int, error) {
	if profileID == "" {
		return 0, errors.New("conversation purge requires profile")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin conversation purge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DELETE FROM conversation_follow_ups WHERE conversation_id IN (
			SELECT c.id FROM conversations c LEFT JOIN applications a ON a.id = c.application_id
			WHERE c.profile_id = ? AND c.application_id <> '' AND a.id IS NULL)`,
		`DELETE FROM conversation_messages WHERE conversation_id IN (
			SELECT c.id FROM conversations c LEFT JOIN applications a ON a.id = c.application_id
			WHERE c.profile_id = ? AND c.application_id <> '' AND a.id IS NULL)`,
		`DELETE FROM conversations WHERE id IN (
			SELECT c.id FROM conversations c LEFT JOIN applications a ON a.id = c.application_id
			WHERE c.profile_id = ? AND c.application_id <> '' AND a.id IS NULL)`,
	} {
		if _, err := tx.ExecContext(ctx, statement, profileID); err != nil {
			return 0, fmt.Errorf("purge orphan conversations: %w", err)
		}
	}
	var removed int
	if err := tx.QueryRowContext(ctx, `SELECT changes()`).Scan(&removed); err != nil {
		return 0, fmt.Errorf("count purged conversations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit conversation purge: %w", err)
	}
	return removed, nil
}

// conversationFilterQuery applies the shared conversation filters so the list
// and the counters never disagree.
func conversationFilterQuery(filter storage.ConversationFilter) (string, []any) {
	query := conversationSelect + ` WHERE 1 = 1`
	args := make([]any, 0, 6)
	if filter.Platform != "" {
		query += ` AND platform = ?`
		args = append(args, filter.Platform)
	}
	if filter.ProfileID != "" {
		query += ` AND profile_id = ?`
		args = append(args, filter.ProfileID)
	}
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	if queryText := strings.TrimSpace(filter.Query); queryText != "" {
		like := "%" + queryText + "%"
		query += ` AND (vacancy_title LIKE ? OR employer LIKE ? OR external_id LIKE ?)`
		args = append(args, like, like, like)
	}
	if filter.UnreadOnly {
		query += ` AND unread_count > 0`
	}
	if filter.QuestionnaireOnly {
		query += ` AND status = 'active' AND EXISTS (
			SELECT 1 FROM conversation_messages m
			WHERE m.conversation_id = conversations.id
			  AND ((m.direction = 'incoming' AND m.kind = 'questionnaire'
			        AND CASE WHEN json_valid(m.options) THEN json_array_length(m.options) ELSE 0 END > 0)
			       OR (m.kind = 'system' AND m.text LIKE '%PARTICIPANT_JOINED%'))
			  AND m.occurred_at > COALESCE((
				SELECT MAX(e.occurred_at) FROM conversation_messages e
				WHERE e.conversation_id = m.conversation_id
				  AND ((e.kind = 'system' AND e.text LIKE '%PARTICIPANT_LEFT%')
				    OR (e.direction = 'incoming' AND e.kind = 'text' AND e.text LIKE '%не готовы пригласить%'))), 0))`
	}
	return query, args
}

// CountConversations returns totals for the same filter as ListConversations.
func (store *Store) CountConversations(ctx context.Context, filter storage.ConversationFilter) (storage.ConversationCounts, error) {
	countQuery := `SELECT COUNT(*), COALESCE(SUM(unread_count), 0), COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0) FROM conversations WHERE 1 = 1`
	filterQuery, args := conversationFilterQuery(filter)
	_, condition, _ := strings.Cut(filterQuery, ` WHERE 1 = 1`)
	var counts storage.ConversationCounts
	if err := store.db.QueryRowContext(ctx, countQuery+condition, args...).Scan(&counts.Total, &counts.Unread, &counts.Active); err != nil {
		return storage.ConversationCounts{}, fmt.Errorf("count conversations: %w", err)
	}
	return counts, nil
}

func (store *Store) AppendConversationMessage(ctx context.Context, message core.ConversationMessage, observedAt time.Time) (core.Conversation, bool, error) {
	if err := message.Validate(); err != nil {
		return core.Conversation{}, false, err
	}
	if observedAt.IsZero() {
		return core.Conversation{}, false, errors.New("message observation requires current time")
	}
	options, err := json.Marshal(message.Options)
	if err != nil {
		return core.Conversation{}, false, fmt.Errorf("encode message options: %w", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Conversation{}, false, fmt.Errorf("begin message append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	conversation, err := scanConversation(tx.QueryRowContext(ctx, conversationSelect+` WHERE id = ?`, message.ConversationID))
	if err != nil {
		return core.Conversation{}, false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO conversation_messages
		(id, conversation_id, external_id, reply_to_id, direction, kind, status, text, options, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, message.ID, message.ConversationID, message.ExternalID,
		message.ReplyToID, message.Direction, message.Kind, message.Status, message.Text, options, message.OccurredAt.UnixNano())
	if err != nil {
		return core.Conversation{}, false, fmt.Errorf("insert conversation message %s: %w", message.ID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil {
		return core.Conversation{}, false, err
	}
	if !created {
		stored, loadErr := loadMessage(ctx, tx, `id = ?`, message.ID)
		if errors.Is(loadErr, sql.ErrNoRows) && message.ExternalID != "" {
			stored, loadErr = loadMessage(ctx, tx, `conversation_id = ? AND external_id = ?`, message.ConversationID, message.ExternalID)
		}
		if loadErr != nil {
			return core.Conversation{}, false, loadErr
		}
		if !sameStoredMessage(stored, message) {
			return core.Conversation{}, false, storage.ErrConversationMessageConflict
		}
		return conversation, false, nil
	}
	expectedRevision := conversation.Revision
	if _, err := conversation.Observe(message, observedAt); err != nil {
		return core.Conversation{}, false, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE conversations SET last_message_id = ?, last_message_at = ?,
		last_incoming_at = ?, last_outgoing_at = ?, updated_at = ?, revision = ?
		WHERE id = ? AND revision = ?`, conversation.LastMessageID, nullableTime(conversation.LastMessageAt),
		nullableTime(conversation.LastIncomingAt), nullableTime(conversation.LastOutgoingAt),
		conversation.UpdatedAt.UnixNano(), conversation.Revision, conversation.ID, expectedRevision)
	if err != nil {
		return core.Conversation{}, false, fmt.Errorf("update conversation after message: %w", err)
	}
	updated, err := oneRowAffected(result)
	if err != nil {
		return core.Conversation{}, false, err
	}
	if !updated {
		return core.Conversation{}, false, storage.ErrRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return core.Conversation{}, false, fmt.Errorf("commit conversation message %s: %w", message.ID, err)
	}
	return conversation, true, nil
}

// OpenQuestionnaireConversationIDs lists conversations whose latest incoming
// questionnaire still has no outgoing answer after it.
func (store *Store) OpenQuestionnaireConversationIDs(ctx context.Context) ([]core.ConversationID, error) {
	// The badge marks a conversation where a questionnaire is still running.
	// It opens with a questionnaire prompt (with options) or with the bot
	// joining the chat (PARTICIPANT_JOINED, free-text questionnaires) and it
	// closes when the bot leaves (PARTICIPANT_LEFT).
	rows, err := store.db.QueryContext(ctx, `
		SELECT DISTINCT m.conversation_id
		FROM conversation_messages m
		JOIN conversations c ON c.id = m.conversation_id
		WHERE c.status = 'active'
		  AND (
			(m.direction = 'incoming' AND m.kind = 'questionnaire'
			 AND CASE WHEN json_valid(m.options) THEN json_array_length(m.options) ELSE 0 END > 0)
			OR (m.kind = 'system' AND m.text LIKE '%PARTICIPANT_JOINED%')
		  )
		  AND m.occurred_at > COALESCE((
			SELECT MAX(e.occurred_at) FROM conversation_messages e
			WHERE e.conversation_id = m.conversation_id
			  AND ((e.kind = 'system' AND e.text LIKE '%PARTICIPANT_LEFT%')
			    OR (e.direction = 'incoming' AND e.kind = 'text' AND e.text LIKE '%не готовы пригласить%'))), 0)
		ORDER BY m.conversation_id`)
	if err != nil {
		return nil, fmt.Errorf("list open questionnaires: %w", err)
	}
	defer rows.Close()
	var result []core.ConversationID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, core.ConversationID(id))
	}
	return result, rows.Err()
}

// ConversationsAwaitingQuestionnaire lists conversations of one profile whose
// latest text-button questionnaire prompt still has no outgoing answer after
// it. Stale prompts outside the recency window are skipped so the scheduler
// does not keep re-reading chats that can no longer be answered.
func (store *Store) ConversationsAwaitingQuestionnaire(ctx context.Context, profileID core.ProfileID, since time.Time, limit int) ([]core.ConversationID, error) {
	if profileID == "" {
		return nil, errors.New("awaiting questionnaire requires a profile")
	}
	if limit <= 0 {
		return nil, nil
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT m.conversation_id
		FROM conversation_messages m
		JOIN conversations c ON c.id = m.conversation_id
		WHERE c.profile_id = ?
		  AND m.direction = 'incoming' AND m.kind = 'questionnaire'
		  AND CASE WHEN json_valid(m.options) THEN json_array_length(m.options) ELSE 0 END > 0
		  AND m.occurred_at >= ?
		  AND m.occurred_at > COALESCE((
			SELECT MAX(o.occurred_at) FROM conversation_messages o
			WHERE o.conversation_id = m.conversation_id AND o.direction = 'outgoing'
			  AND o.status IN ('sent', 'queued')), 0)
		GROUP BY m.conversation_id
		ORDER BY MAX(m.occurred_at) DESC
		LIMIT ?`, profileID, since.UnixNano(), limit)
	if err != nil {
		return nil, fmt.Errorf("list conversations awaiting questionnaire: %w", err)
	}
	defer rows.Close()
	result := make([]core.ConversationID, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, core.ConversationID(id))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversations awaiting questionnaire: %w", err)
	}
	return result, nil
}

func (store *Store) ConversationMessages(ctx context.Context, id core.ConversationID) ([]core.ConversationMessage, error) {
	if id == "" {
		return nil, errors.New("conversation id is required")
	}
	rows, err := store.db.QueryContext(ctx, messageSelect+` WHERE conversation_id = ? ORDER BY occurred_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("list conversation messages: %w", err)
	}
	defer rows.Close()
	result := make([]core.ConversationMessage, 0)
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation messages: %w", err)
	}
	return result, nil
}

func (store *Store) CreateFollowUp(ctx context.Context, candidate core.FollowUp) (core.FollowUp, bool, error) {
	if err := candidate.Validate(); err != nil {
		return core.FollowUp{}, false, err
	}
	if candidate.Status != core.FollowUpScheduled || candidate.Revision != 1 {
		return core.FollowUp{}, false, errors.New("follow-up repository accepts only initialized scheduled follow-ups")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.FollowUp{}, false, fmt.Errorf("begin follow-up create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	conversation, err := scanConversation(tx.QueryRowContext(ctx, conversationSelect+` WHERE id = ?`, candidate.ConversationID))
	if err != nil {
		return core.FollowUp{}, false, err
	}
	if conversation.ProfileID != candidate.ProfileID || conversation.Platform != candidate.Platform {
		return core.FollowUp{}, false, errors.New("follow-up conversation identity mismatch")
	}
	result, err := tx.ExecContext(ctx, followUpInsert, followUpValues(candidate)...)
	if err != nil {
		return core.FollowUp{}, false, fmt.Errorf("create follow-up %s: %w", candidate.ID, err)
	}
	created, err := oneRowAffected(result)
	if err != nil {
		return core.FollowUp{}, false, err
	}
	if created {
		if err := tx.Commit(); err != nil {
			return core.FollowUp{}, false, fmt.Errorf("commit follow-up %s: %w", candidate.ID, err)
		}
		return candidate, true, nil
	}
	stored, loadErr := scanFollowUp(tx.QueryRowContext(ctx, followUpSelect+` WHERE id = ?`, candidate.ID))
	if errors.Is(loadErr, sql.ErrNoRows) {
		stored, loadErr = scanFollowUp(tx.QueryRowContext(ctx, followUpSelect+` WHERE idempotency_key = ?`, candidate.IdempotencyKey))
	}
	if loadErr != nil {
		return core.FollowUp{}, false, loadErr
	}
	if stored.ID == candidate.ID && (stored.IdempotencyKey != candidate.IdempotencyKey || !sameFollowUpRequest(stored, candidate)) {
		return core.FollowUp{}, false, errors.New("follow-up id conflicts with a different timer")
	}
	if stored.ID != candidate.ID && !sameFollowUpRequest(stored, candidate) {
		return core.FollowUp{}, false, errors.New("follow-up idempotency key conflicts with a different timer")
	}
	return stored, false, nil
}

func (store *Store) FollowUp(ctx context.Context, id core.FollowUpID) (core.FollowUp, error) {
	if id == "" {
		return core.FollowUp{}, errors.New("follow-up id is required")
	}
	return scanFollowUp(store.db.QueryRowContext(ctx, followUpSelect+` WHERE id = ?`, id))
}

func (store *Store) SaveFollowUp(ctx context.Context, candidate core.FollowUp, expectedRevision uint64) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.Revision != expectedRevision+1 {
		return errors.New("follow-up candidate must advance revision exactly once")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE conversation_follow_ups SET
		anchor_message_id = ?, anchor_at = ?, run_at = ?, deadline = ?, content_text = ?,
		content_template = ?, content_operator = ?, cancel_on_incoming = ?,
		require_active_conversation = ?, max_follow_ups = ?, cooldown = ?, status = ?,
		sent_message_id = ?, cancel_reason = ?, failure_message = ?, updated_at = ?, revision = ?
		WHERE id = ? AND conversation_id = ? AND profile_id = ? AND platform = ?
		AND idempotency_key = ? AND created_at = ? AND revision = ?`,
		candidate.AnchorMessageID, candidate.AnchorAt.UnixNano(), candidate.RunAt.UnixNano(), nullableTime(candidate.Deadline),
		candidate.Content.Text, candidate.Content.TemplateTag, candidate.Content.OperatorTag,
		candidate.Policy.CancelOnIncoming, candidate.Policy.RequireActiveConversation, candidate.Policy.MaxFollowUps,
		int64(candidate.Policy.Cooldown), candidate.Status, candidate.SentMessageID, candidate.CancelReason,
		candidate.FailureMessage, candidate.UpdatedAt.UnixNano(), candidate.Revision, candidate.ID,
		candidate.ConversationID, candidate.ProfileID, candidate.Platform, candidate.IdempotencyKey,
		candidate.CreatedAt.UnixNano(), expectedRevision)
	if err != nil {
		return fmt.Errorf("save follow-up %s: %w", candidate.ID, err)
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

func (store *Store) ListFollowUps(ctx context.Context, filter storage.FollowUpFilter) ([]core.FollowUp, error) {
	query := followUpSelect + ` WHERE 1 = 1`
	args := make([]any, 0, 4)
	if filter.ConversationID != "" {
		query += ` AND conversation_id = ?`
		args = append(args, filter.ConversationID)
	}
	if filter.ProfileID != "" {
		query += ` AND profile_id = ?`
		args = append(args, filter.ProfileID)
	}
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	if filter.DueBefore != nil {
		query += ` AND run_at <= ?`
		args = append(args, filter.DueBefore.UnixNano())
	}
	query += ` ORDER BY run_at, id`
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list follow-ups: %w", err)
	}
	defer rows.Close()
	result := make([]core.FollowUp, 0)
	for rows.Next() {
		followUp, err := scanFollowUp(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, followUp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate follow-ups: %w", err)
	}
	return result, nil
}

const conversationSelect = `SELECT id, platform, profile_id, external_id, application_id, vacancy_title, employer, vacancy_url, unread_count, status,
	last_message_id, last_message_at, last_incoming_at, last_outgoing_at, last_read_at, created_at, updated_at, revision FROM conversations`

const messageSelect = `SELECT id, conversation_id, external_id, reply_to_id, direction, kind, status,
	text, options, occurred_at FROM conversation_messages`

const followUpSelect = `SELECT id, conversation_id, profile_id, platform, anchor_message_id, anchor_at,
	run_at, deadline, content_text, content_template, content_operator, cancel_on_incoming,
	require_active_conversation, max_follow_ups, cooldown, status, idempotency_key,
	sent_message_id, cancel_reason, failure_message, created_at, updated_at, revision FROM conversation_follow_ups`

const followUpInsert = `INSERT OR IGNORE INTO conversation_follow_ups
	(id, conversation_id, profile_id, platform, anchor_message_id, anchor_at, run_at, deadline,
	 content_text, content_template, content_operator, cancel_on_incoming, require_active_conversation,
	 max_follow_ups, cooldown, status, idempotency_key, sent_message_id, cancel_reason,
	 failure_message, created_at, updated_at, revision)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func followUpValues(followUp core.FollowUp) []any {
	return []any{
		followUp.ID, followUp.ConversationID, followUp.ProfileID, followUp.Platform,
		followUp.AnchorMessageID, followUp.AnchorAt.UnixNano(), followUp.RunAt.UnixNano(), nullableTime(followUp.Deadline),
		followUp.Content.Text, followUp.Content.TemplateTag, followUp.Content.OperatorTag,
		followUp.Policy.CancelOnIncoming, followUp.Policy.RequireActiveConversation,
		followUp.Policy.MaxFollowUps, int64(followUp.Policy.Cooldown), followUp.Status,
		followUp.IdempotencyKey, followUp.SentMessageID, followUp.CancelReason, followUp.FailureMessage,
		followUp.CreatedAt.UnixNano(), followUp.UpdatedAt.UnixNano(), followUp.Revision,
	}
}

func scanConversation(row rowScanner) (core.Conversation, error) {
	var conversation core.Conversation
	var lastMessageAt, lastIncomingAt, lastOutgoingAt, lastReadAt sql.NullInt64
	var createdAt, updatedAt int64
	if err := row.Scan(&conversation.ID, &conversation.Platform, &conversation.ProfileID, &conversation.ExternalID,
		&conversation.ApplicationID, &conversation.VacancyTitle, &conversation.Employer, &conversation.VacancyURL,
		&conversation.UnreadCount, &conversation.Status, &conversation.LastMessageID, &lastMessageAt,
		&lastIncomingAt, &lastOutgoingAt, &lastReadAt, &createdAt, &updatedAt, &conversation.Revision); err != nil {
		return core.Conversation{}, err
	}
	conversation.LastMessageAt = timeFromNull(lastMessageAt)
	conversation.LastIncomingAt = timeFromNull(lastIncomingAt)
	conversation.LastOutgoingAt = timeFromNull(lastOutgoingAt)
	conversation.LastReadAt = timeFromNull(lastReadAt)
	conversation.CreatedAt = time.Unix(0, createdAt).UTC()
	conversation.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return conversation, conversation.Validate()
}

func loadMessage(ctx context.Context, tx *sql.Tx, condition string, args ...any) (core.ConversationMessage, error) {
	return scanMessage(tx.QueryRowContext(ctx, messageSelect+` WHERE `+condition, args...))
}

func scanMessage(row rowScanner) (core.ConversationMessage, error) {
	var message core.ConversationMessage
	var options []byte
	var occurredAt int64
	if err := row.Scan(&message.ID, &message.ConversationID, &message.ExternalID, &message.ReplyToID,
		&message.Direction, &message.Kind, &message.Status, &message.Text, &options, &occurredAt); err != nil {
		return core.ConversationMessage{}, err
	}
	if err := json.Unmarshal(options, &message.Options); err != nil {
		return core.ConversationMessage{}, fmt.Errorf("decode message options: %w", err)
	}
	message.OccurredAt = time.Unix(0, occurredAt).UTC()
	return message, message.Validate()
}

func scanFollowUp(row rowScanner) (core.FollowUp, error) {
	var followUp core.FollowUp
	var anchorAt, runAt, cooldown, createdAt, updatedAt int64
	var deadline sql.NullInt64
	if err := row.Scan(&followUp.ID, &followUp.ConversationID, &followUp.ProfileID, &followUp.Platform,
		&followUp.AnchorMessageID, &anchorAt, &runAt, &deadline, &followUp.Content.Text,
		&followUp.Content.TemplateTag, &followUp.Content.OperatorTag, &followUp.Policy.CancelOnIncoming,
		&followUp.Policy.RequireActiveConversation, &followUp.Policy.MaxFollowUps, &cooldown,
		&followUp.Status, &followUp.IdempotencyKey, &followUp.SentMessageID, &followUp.CancelReason,
		&followUp.FailureMessage, &createdAt, &updatedAt, &followUp.Revision); err != nil {
		return core.FollowUp{}, err
	}
	followUp.AnchorAt = time.Unix(0, anchorAt).UTC()
	followUp.RunAt = time.Unix(0, runAt).UTC()
	followUp.Deadline = timeFromNull(deadline)
	followUp.Policy.Cooldown = core.Duration(cooldown)
	followUp.CreatedAt = time.Unix(0, createdAt).UTC()
	followUp.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return followUp, followUp.Validate()
}

func sameConversationIdentity(first, second core.Conversation) bool {
	return first.ID == second.ID && first.Platform == second.Platform && first.ProfileID == second.ProfileID &&
		first.ExternalID == second.ExternalID && first.ApplicationID == second.ApplicationID
}

func sameStoredMessage(stored, candidate core.ConversationMessage) bool {
	if stored.ConversationID != candidate.ConversationID || stored.ExternalID != candidate.ExternalID ||
		!sameMessageReply(stored.ReplyToID, candidate.ReplyToID) || stored.Direction != candidate.Direction || stored.Kind != candidate.Kind ||
		stored.Status != candidate.Status || stored.Text != candidate.Text || !sameMessageTime(stored.OccurredAt, candidate.OccurredAt) ||
		len(stored.Options) != len(candidate.Options) {
		return false
	}
	for index := range stored.Options {
		if stored.Options[index] != candidate.Options[index] {
			return false
		}
	}
	return stored.ID == candidate.ID || stored.ExternalID != ""
}

// sameMessageReply tolerates a missing reply link on one side: the local send
// stores the prompt it answered, while synced platform history often omits it.
func sameMessageReply(first, second core.MessageID) bool {
	return first == second || first == "" || second == ""
}

// sameMessageTime compares platform timestamps at millisecond precision: HH
// returns milliseconds for synced history but can carry sub-millisecond
// fractions on a send response for the very same message.
func sameMessageTime(first, second time.Time) bool {
	return first.Truncate(time.Millisecond).Equal(second.Truncate(time.Millisecond))
}

func sameFollowUpRequest(first, second core.FollowUp) bool {
	return first.ConversationID == second.ConversationID && first.ProfileID == second.ProfileID &&
		first.Platform == second.Platform && first.AnchorMessageID == second.AnchorMessageID &&
		first.AnchorAt.Equal(second.AnchorAt) && first.RunAt.Equal(second.RunAt) && equalNullableTime(first.Deadline, second.Deadline) &&
		first.Content == second.Content && first.Policy == second.Policy
}

func equalNullableTime(first, second *time.Time) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.Equal(*second)
}
