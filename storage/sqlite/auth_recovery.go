package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
)

// FailOpenAuthSessions marks sessions that were mid-flight when the process
// stopped. Challenges live only in memory, so a restarted backend cannot
// continue them; the client must start a new login.
func (store *Store) FailOpenAuthSessions(ctx context.Context, now time.Time, message string) (int, error) {
	if now.IsZero() {
		return 0, errors.New("auth session recovery requires current time")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return 0, errors.New("auth session recovery requires message")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE auth_sessions SET
		status = ?, failure_category = ?, failure_message = ?,
		revision = revision + 1, updated_at = ?
		WHERE status NOT IN (?, ?, ?, ?)`,
		core.AuthSessionFailed, core.ErrorTemporaryFailure, message, now.UnixNano(),
		core.AuthSessionCompleted, core.AuthSessionExpired, core.AuthSessionCancelled, core.AuthSessionFailed)
	if err != nil {
		return 0, fmt.Errorf("fail open auth sessions: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}
