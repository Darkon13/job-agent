package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
)

// SaveProfileIdentity stores the account summary captured after a sign-in. It
// is idempotent: a newer capture replaces the previous one.
func (store *Store) SaveProfileIdentity(ctx context.Context, profileID core.ProfileID, identity core.ProfileIdentity, now time.Time) error {
	if profileID == "" || now.IsZero() {
		return errors.New("profile identity requires profile and capture time")
	}
	capturedAt := identity.CapturedAt
	if capturedAt.IsZero() {
		capturedAt = now
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO profile_identities
		(profile_id, display_name, email, phone, account_hash, captured_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id) DO UPDATE SET
		 display_name = excluded.display_name, email = excluded.email, phone = excluded.phone,
		 account_hash = excluded.account_hash, captured_at = excluded.captured_at, updated_at = excluded.updated_at`,
		profileID, identity.DisplayName, identity.Email, identity.Phone,
		identity.AccountHash, capturedAt.UnixNano(), now.UnixNano()); err != nil {
		return fmt.Errorf("save profile identity %s: %w", profileID, err)
	}
	return nil
}

// ProfileIdentity returns the stored account summary of one profile.
func (store *Store) ProfileIdentity(ctx context.Context, profileID core.ProfileID) (core.ProfileIdentity, bool, error) {
	if profileID == "" {
		return core.ProfileIdentity{}, false, errors.New("profile identity requires a profile")
	}
	row := store.db.QueryRowContext(ctx, `SELECT display_name, email, phone, account_hash, captured_at
		FROM profile_identities WHERE profile_id = ?`, profileID)
	var identity core.ProfileIdentity
	var capturedAt int64
	if err := row.Scan(&identity.DisplayName, &identity.Email, &identity.Phone, &identity.AccountHash, &capturedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.ProfileIdentity{}, false, nil
		}
		return core.ProfileIdentity{}, false, fmt.Errorf("load profile identity %s: %w", profileID, err)
	}
	identity.CapturedAt = time.Unix(0, capturedAt).UTC()
	return identity, true, nil
}
