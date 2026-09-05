package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// SearchRun is the durable cursor owner for one configured search. A page task
// may be retried freely, but only a successful page advances Revision/Cursor.
type SearchRun struct {
	SearchID        SearchID        `json:"search_id"`
	Adapter         string          `json:"adapter"`
	Platform        Platform        `json:"platform"`
	SearchProfileID ProfileID       `json:"search_profile_id"`
	TargetProfiles  []ProfileID     `json:"target_profiles"`
	CorrelationID   CorrelationID   `json:"correlation_id"`
	Query           json.RawMessage `json:"query"`
	Cursor          string          `json:"cursor,omitempty"`
	Done            bool            `json:"done"`
	Revision        uint64          `json:"revision"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func NewSearchRun(searchID SearchID, adapter string, platform Platform, searchProfileID ProfileID, targetProfiles []ProfileID, query json.RawMessage, correlationID CorrelationID, now time.Time) (SearchRun, error) {
	run := SearchRun{
		SearchID: searchID, Adapter: adapter, Platform: platform, SearchProfileID: searchProfileID,
		TargetProfiles: slices.Clone(targetProfiles), Query: append(json.RawMessage(nil), query...),
		CorrelationID: correlationID,
		Revision:      1, CreatedAt: now, UpdatedAt: now,
	}
	if err := run.Validate(); err != nil {
		return SearchRun{}, err
	}
	return run, nil
}

func (run SearchRun) Validate() error {
	if run.SearchID == "" || strings.TrimSpace(run.Adapter) == "" || run.Platform == "" || run.SearchProfileID == "" || run.CorrelationID == "" {
		return errors.New("search run requires search id, adapter, platform, search profile and correlation id")
	}
	if len(run.TargetProfiles) == 0 {
		return errors.New("search run requires target profiles")
	}
	seen := make(map[ProfileID]struct{}, len(run.TargetProfiles))
	for _, profileID := range run.TargetProfiles {
		if profileID == "" {
			return errors.New("search run contains empty target profile")
		}
		if _, exists := seen[profileID]; exists {
			return fmt.Errorf("search run contains duplicate target profile %q", profileID)
		}
		seen[profileID] = struct{}{}
	}
	if len(run.Query) == 0 || !json.Valid(run.Query) {
		return errors.New("search run requires valid JSON query")
	}
	if run.Done && run.Cursor != "" {
		return errors.New("completed search run must not retain a cursor")
	}
	if run.Revision == 0 || run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() || run.UpdatedAt.Before(run.CreatedAt) {
		return errors.New("search run requires valid revision and timestamps")
	}
	return nil
}

func (run *SearchRun) Advance(nextCursor string, done bool, now time.Time) error {
	if run == nil {
		return errors.New("search run is nil")
	}
	if run.Done {
		return errors.New("completed search run cannot advance")
	}
	if done && nextCursor != "" {
		return errors.New("completed search page must not contain next cursor")
	}
	if !done && nextCursor == "" {
		return errors.New("incomplete search page requires next cursor")
	}
	if now.IsZero() || now.Before(run.UpdatedAt) {
		return errors.New("search run update time must not move backwards")
	}
	run.Cursor = nextCursor
	run.Done = done
	run.Revision++
	run.UpdatedAt = now
	return run.Validate()
}

type SearchPagePayload struct {
	SearchID SearchID `json:"search_id"`
	Cursor   string   `json:"cursor,omitempty"`
}

func (payload SearchPagePayload) Validate() error {
	if payload.SearchID == "" {
		return errors.New("search page task requires search id")
	}
	return nil
}

func SearchPageIdempotencyKey(searchID SearchID, cursor string) (string, error) {
	if searchID == "" {
		return "", errors.New("search page idempotency key requires search id")
	}
	digest := sha256.Sum256([]byte(cursor))
	return "search-page:" + string(searchID) + ":" + hex.EncodeToString(digest[:8]), nil
}
