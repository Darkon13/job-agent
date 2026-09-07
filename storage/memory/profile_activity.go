package memory

import (
	"context"
	"errors"
	"sort"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func (repository *Repository) RecordProfileActivity(ctx context.Context, candidate core.ProfileActivityRecord) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := candidate.Validate(); err != nil {
		return false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.profileActivity[candidate.ID]
	if exists {
		if stored.Platform != candidate.Platform || stored.ProfileID != candidate.ProfileID || stored.ResumeID != candidate.ResumeID ||
			stored.Kind != candidate.Kind || stored.SourceID != candidate.SourceID {
			return false, errors.New("profile activity id conflicts with different identity")
		}
		return false, nil
	}
	repository.profileActivity[candidate.ID] = candidate
	return true, nil
}

func (repository *Repository) ListProfileActivity(ctx context.Context, filter storage.ProfileActivityFilter) ([]core.ProfileActivityRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.ProfileActivityRecord, 0)
	for _, record := range repository.profileActivity {
		if !profileActivityMatches(record, filter) {
			continue
		}
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].OccurredAt.After(result[j].OccurredAt)
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func (repository *Repository) ProfileActivityCounts(ctx context.Context, filter storage.ProfileActivityFilter) ([]storage.ProfileActivityCount, error) {
	records, err := repository.ListProfileActivity(ctx, filter)
	if err != nil {
		return nil, err
	}
	type key struct {
		platform  core.Platform
		profileID core.ProfileID
		kind      core.ProfileActivityKind
	}
	counts := make(map[key]storage.ProfileActivityCount)
	for _, record := range records {
		identity := key{platform: record.Platform, profileID: record.ProfileID, kind: record.Kind}
		item := counts[identity]
		item.Platform = record.Platform
		item.ProfileID = record.ProfileID
		item.Kind = record.Kind
		item.Count++
		if record.OccurredAt.After(item.LastOccurredAt) {
			item.LastOccurredAt = record.OccurredAt
		}
		counts[identity] = item
	}
	result := make([]storage.ProfileActivityCount, 0, len(counts))
	for _, item := range counts {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ProfileID != result[j].ProfileID {
			return result[i].ProfileID < result[j].ProfileID
		}
		if result[i].Platform != result[j].Platform {
			return result[i].Platform < result[j].Platform
		}
		return result[i].Kind < result[j].Kind
	})
	return result, nil
}

func profileActivityMatches(record core.ProfileActivityRecord, filter storage.ProfileActivityFilter) bool {
	return (filter.Platform == "" || record.Platform == filter.Platform) &&
		(filter.ProfileID == "" || record.ProfileID == filter.ProfileID) &&
		(filter.Kind == "" || record.Kind == filter.Kind)
}

func (repository *Repository) RecordProfileActivitySnapshot(ctx context.Context, candidate core.ProfileActivitySnapshot) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := candidate.Validate(); err != nil {
		return false, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	stored, exists := repository.activitySnapshots[candidate.ID]
	if exists {
		if !sameProfileActivitySnapshotIdentity(stored, candidate) {
			return false, errors.New("profile activity snapshot id conflicts with different identity")
		}
		return false, nil
	}
	repository.activitySnapshots[candidate.ID] = cloneProfileActivitySnapshot(candidate)
	return true, nil
}

func (repository *Repository) ListProfileActivitySnapshots(ctx context.Context, filter storage.ProfileActivitySnapshotFilter) ([]core.ProfileActivitySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filter.Limit < 0 {
		return nil, errors.New("profile activity snapshot limit cannot be negative")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]core.ProfileActivitySnapshot, 0)
	for _, snapshot := range repository.activitySnapshots {
		if (filter.Platform != "" && snapshot.Platform != filter.Platform) ||
			(filter.ProfileID != "" && snapshot.ProfileID != filter.ProfileID) ||
			(filter.ResumeID != "" && snapshot.ResumeID != filter.ResumeID) {
			continue
		}
		result = append(result, cloneProfileActivitySnapshot(snapshot))
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].ObservedAt.Equal(result[j].ObservedAt) {
			return result[i].ObservedAt.After(result[j].ObservedAt)
		}
		return result[i].ID < result[j].ID
	})
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func sameProfileActivitySnapshotIdentity(left, right core.ProfileActivitySnapshot) bool {
	return left.Platform == right.Platform && left.ProfileID == right.ProfileID && left.ResumeID == right.ResumeID && left.SourceID == right.SourceID
}

func cloneProfileActivitySnapshot(snapshot core.ProfileActivitySnapshot) core.ProfileActivitySnapshot {
	snapshot.Score = cloneInt(snapshot.Score)
	snapshot.PeriodDays = cloneInt(snapshot.PeriodDays)
	snapshot.SearchShows = cloneInt(snapshot.SearchShows)
	snapshot.Views = cloneInt(snapshot.Views)
	snapshot.NewViews = cloneInt(snapshot.NewViews)
	snapshot.Invitations = cloneInt(snapshot.Invitations)
	snapshot.NewInvitations = cloneInt(snapshot.NewInvitations)
	snapshot.ResponseStreak = cloneInt(snapshot.ResponseStreak)
	snapshot.ResponsesRequired = cloneInt(snapshot.ResponsesRequired)
	return snapshot
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
