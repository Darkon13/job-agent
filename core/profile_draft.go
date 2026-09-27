package core

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ProfileDraftStatus tracks one dashboard-managed profile from its first login
// to the moment its fragment lands in the profile store.
type ProfileDraftStatus string

const (
	// ProfileDraftPending means the interactive login has not completed yet.
	ProfileDraftPending ProfileDraftStatus = "pending"
	// ProfileDraftReady means the account is known and the operator still picks
	// resumes and applies the fragment.
	ProfileDraftReady ProfileDraftStatus = "ready"
	// ProfileDraftApplied means the fragment was written; the draft stays as
	// history until the operator removes it.
	ProfileDraftApplied ProfileDraftStatus = "applied"
)

// ProfileDraftTagPattern bounds dashboard-created profile tags: a lower-case
// slug that is safe as a file name, as a job tag and in URLs.
var ProfileDraftTagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ProfileDraftResume is one resume observed after a successful login.
type ProfileDraftResume struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// ProfileIdentity is the safe account summary captured after login: the name,
// masked contacts and a short account fingerprint. Tokens, cookies and full
// contact values never enter this record.
type ProfileIdentity struct {
	DisplayName string    `json:"display_name,omitempty"`
	Email       string    `json:"email,omitempty"`
	Phone       string    `json:"phone,omitempty"`
	AccountHash string    `json:"account_hash,omitempty"`
	CapturedAt  time.Time `json:"captured_at,omitempty"`
}

// ProfileDraft is a profile that exists only in the local database until its
// fragment is applied. The draft owns the state file path, so no client ever
// picks a path on disk.
type ProfileDraft struct {
	Tag       string               `json:"tag"`
	Platform  Platform             `json:"platform"`
	Adapter   string               `json:"adapter"`
	StateFile string               `json:"state_file"`
	Identity  *ProfileIdentity     `json:"identity,omitempty"`
	Resumes   []ProfileDraftResume `json:"resumes"`
	Status    ProfileDraftStatus   `json:"status"`
	CreatedAt time.Time            `json:"created_at"`
	UpdatedAt time.Time            `json:"updated_at"`
	Revision  uint64               `json:"revision"`
}

// ValidateProfileDraftTag checks the slug used for a dashboard-managed profile.
func ValidateProfileDraftTag(tag string) error {
	if !ProfileDraftTagPattern.MatchString(tag) {
		return fmt.Errorf("profile tag %q must be a lower-case slug of 1..64 characters (a-z, 0-9, -, _)", tag)
	}
	return nil
}

func (draft ProfileDraft) Validate() error {
	if err := ValidateProfileDraftTag(draft.Tag); err != nil {
		return err
	}
	if strings.TrimSpace(string(draft.Platform)) == "" || strings.TrimSpace(draft.Adapter) == "" {
		return errors.New("profile draft requires platform and adapter")
	}
	if strings.TrimSpace(draft.StateFile) == "" {
		return errors.New("profile draft requires a state file")
	}
	switch draft.Status {
	case ProfileDraftPending, ProfileDraftReady, ProfileDraftApplied:
	default:
		return fmt.Errorf("profile draft has invalid status %q", draft.Status)
	}
	if draft.CreatedAt.IsZero() || draft.UpdatedAt.IsZero() {
		return errors.New("profile draft requires timestamps")
	}
	if draft.Revision == 0 {
		return errors.New("profile draft requires a revision")
	}
	return nil
}

// NewProfileDraft creates a pending draft at the given moment.
func NewProfileDraft(tag string, platform Platform, adapter, stateFile string, now time.Time) (ProfileDraft, error) {
	draft := ProfileDraft{
		Tag: tag, Platform: platform, Adapter: adapter, StateFile: stateFile,
		Status: ProfileDraftPending, CreatedAt: now.UTC(), UpdatedAt: now.UTC(), Revision: 1,
	}
	return draft, draft.Validate()
}

// ObserveIdentity stores the captured account summary and moves the draft to
// ready. Repeating the observation only refreshes the record while the draft
// has not been applied yet.
func (draft *ProfileDraft) ObserveIdentity(identity ProfileIdentity, resumes []ProfileDraftResume, now time.Time) error {
	if draft.Status == ProfileDraftApplied {
		return errors.New("an applied profile draft keeps its identity")
	}
	if identity.CapturedAt.IsZero() {
		identity.CapturedAt = now.UTC()
	}
	copied := identity
	draft.Identity = &copied
	draft.Resumes = append([]ProfileDraftResume(nil), resumes...)
	draft.Status = ProfileDraftReady
	draft.UpdatedAt = now.UTC()
	draft.Revision++
	return draft.Validate()
}

// MarkApplied records that the profile fragment was written to the store.
func (draft *ProfileDraft) MarkApplied(now time.Time) error {
	draft.Status = ProfileDraftApplied
	draft.UpdatedAt = now.UTC()
	draft.Revision++
	return draft.Validate()
}
