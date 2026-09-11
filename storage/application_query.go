package storage

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
)

// Query projections exclude cover letters, credentials and task payloads.
type ApplicationListEntry struct {
	ID           core.ApplicationID
	ProfileID    core.ProfileID
	Status       core.ApplicationStatus
	DecisionCode string
	Disposition  core.ApplicationDisposition
	Title        string
	Employer     string
	UpdatedAt    time.Time
}

type ApplicationQuery struct {
	ProfileID                    core.ProfileID
	Status                       core.ApplicationStatus
	Query, Employer, Group, Sort string
	Offset, Limit                int
}

type ApplicationPage struct {
	IDs    []core.ApplicationID
	Total  int
	Groups map[string]int
}

type ApplicationQueryRepository interface {
	QueryApplications(context.Context, ApplicationQuery) (ApplicationPage, error)
}

func (query ApplicationQuery) Validate() error {
	if query.Limit < 1 || query.Limit > 200 || query.Offset < 0 {
		return errors.New("application page requires limit 1..200 and nonnegative offset")
	}
	if len(query.Query) > 500 || len(query.Employer) > 500 {
		return errors.New("application query is too long")
	}
	switch query.Sort {
	case "", "updated_desc", "updated_asc", "employer_asc", "title_asc":
	default:
		return errors.New("invalid application sort")
	}
	switch query.Group {
	case "", "sent", "queued", "needs_input", "waiting_invitation", "invited", "rejected", "state_unknown", "hidden", "not_sent":
	default:
		return errors.New("invalid application group")
	}
	return nil
}

func ApplicationListGroup(item ApplicationListEntry) string {
	if item.Status == core.ApplicationSubmitted || item.DecisionCode == "already_applied" {
		switch item.Disposition {
		case core.ApplicationDispositionPending:
			return "waiting_invitation"
		case core.ApplicationDispositionInvited:
			return "invited"
		case core.ApplicationDispositionRejected:
			return "rejected"
		case core.ApplicationDispositionHidden:
			return "hidden"
		default:
			return "state_unknown"
		}
	}
	if item.Status == core.ApplicationWaitingValidation {
		switch item.DecisionCode {
		case "questionnaire_required", "vacancy_test_required", "platform_validation_required":
			return "needs_input"
		}
	}
	switch item.Status {
	case core.ApplicationNew, core.ApplicationPreparing, core.ApplicationReady, core.ApplicationSubmitting, core.ApplicationPendingReconcile:
		return "queued"
	}
	return "not_sent"
}

// Filtering precedes pagination, including Unicode case folding. This keeps
// SQLite and memory identical (SQLite LOWER alone only folds ASCII). The scan
// uses lightweight metadata, never prepared letters/resume snapshots.
func SelectApplicationPage(entries []ApplicationListEntry, query ApplicationQuery) ApplicationPage {
	result := ApplicationPage{IDs: make([]core.ApplicationID, 0), Groups: make(map[string]int)}
	matched := make([]ApplicationListEntry, 0)
	needle, employer := strings.ToLower(strings.TrimSpace(query.Query)), strings.ToLower(strings.TrimSpace(query.Employer))
	for _, entry := range entries {
		if query.ProfileID != "" && entry.ProfileID != query.ProfileID || query.Status != "" && entry.Status != query.Status {
			continue
		}
		if !strings.Contains(strings.ToLower(entry.Employer), employer) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(entry.Title+"\n"+entry.Employer+"\n"+string(entry.ProfileID)+"\n"+entry.DecisionCode), needle) {
			continue
		}
		group := ApplicationListGroup(entry)
		result.Groups[group]++
		result.Groups[""]++
		sent := entry.Status == core.ApplicationSubmitted || entry.DecisionCode == "already_applied"
		if sent {
			result.Groups["sent"]++
		}
		if query.Group != "" && group != query.Group && !(query.Group == "sent" && sent) {
			continue
		}
		matched = append(matched, entry)
	}
	sort.Slice(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		if query.Sort == "employer_asc" && strings.ToLower(a.Employer) != strings.ToLower(b.Employer) {
			return strings.ToLower(a.Employer) < strings.ToLower(b.Employer)
		}
		if query.Sort == "title_asc" && strings.ToLower(a.Title) != strings.ToLower(b.Title) {
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			if query.Sort == "updated_asc" {
				return a.UpdatedAt.Before(b.UpdatedAt)
			}
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID < b.ID
	})
	result.Total = len(matched)
	start := min(query.Offset, len(matched))
	for _, entry := range matched[start : start+min(query.Limit, len(matched)-start)] {
		result.IDs = append(result.IDs, entry.ID)
	}
	return result
}
