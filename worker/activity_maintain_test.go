package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type maintainSearcher struct {
	pages map[string]core.SearchPage
	calls []string
}

func (searcher *maintainSearcher) ValidateSearch(json.RawMessage) error { return nil }

func (searcher *maintainSearcher) Search(_ context.Context, _ core.ProfileID, _ json.RawMessage, cursor string) (core.SearchPage, error) {
	searcher.calls = append(searcher.calls, cursor)
	return searcher.pages[cursor], nil
}

type maintainReader struct{}

func (maintainReader) ReadVacancy(_ context.Context, _ core.ProfileID, key core.VacancyKey) (core.Vacancy, error) {
	return core.Vacancy{
		Platform: key.Platform, ExternalID: key.ExternalID, Title: "Vacancy",
		State: core.VacancyStateOpen, ObservedAt: time.Now().UTC(),
	}, nil
}

type maintainActivity struct {
	*storagememory.Repository
	records  []core.ProfileActivityRecord
	recorded []string
}

func (repository *maintainActivity) ListProfileActivity(context.Context, storage.ProfileActivityFilter) ([]core.ProfileActivityRecord, error) {
	return repository.records, nil
}

func (repository *maintainActivity) RecordProfileActivity(_ context.Context, candidate core.ProfileActivityRecord) (bool, error) {
	repository.records = append(repository.records, candidate)
	repository.recorded = append(repository.recorded, candidate.SourceID)
	return true, nil
}

func maintainHandler(t *testing.T, now time.Time, searcher *maintainSearcher, activity *maintainActivity) *ActivityMaintainHandler {
	t.Helper()
	transports := NewApplicationTransportRegistry()
	if err := transports.RegisterVacancyReader("primary", maintainReader{}); err != nil {
		t.Fatalf("register reader: %v", err)
	}
	if err := transports.RegisterVacancySearcher("primary", searcher); err != nil {
		t.Fatalf("register searcher: %v", err)
	}
	handler, err := NewActivityMaintainHandler(activity.Repository, transports, activity, activity.Repository, &conversationClock{now: now})
	if err != nil {
		t.Fatalf("new maintain handler: %v", err)
	}
	return handler
}

func maintainTask(t *testing.T, count int) core.Task {
	t.Helper()
	payload, err := json.Marshal(core.ProfileActivityMaintainPayload{
		ProfileID: "primary", Count: count, Query: json.RawMessage(`{"source":"global"}`),
	})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return core.Task{ID: "maintain-1", Type: core.TaskProfileActivityMaintain, ProfileID: "primary", Platform: "hh", Payload: payload}
}

func TestActivityMaintainPagesPastInspectedVacancies(t *testing.T) {
	now := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	activity := &maintainActivity{Repository: repository, records: []core.ProfileActivityRecord{
		{ProfileID: "primary", Kind: core.ProfileActivityVacancyInspected, SourceID: "v1", OccurredAt: now.Add(-time.Hour)},
	}}
	searcher := &maintainSearcher{pages: map[string]core.SearchPage{
		"": {Vacancies: []core.Vacancy{{Platform: "hh", ExternalID: "v1", State: core.VacancyStateOpen}}, NextCursor: "next"},
		"next": {Vacancies: []core.Vacancy{
			{Platform: "hh", ExternalID: "v2", State: core.VacancyStateOpen},
			{Platform: "hh", ExternalID: "v3", State: core.VacancyStateOpen},
		}, Done: true},
	}}
	handler := maintainHandler(t, now, searcher, activity)
	if err := handler.Handle(context.Background(), maintainTask(t, 2)); err != nil {
		t.Fatalf("handle maintain: %v", err)
	}
	if len(activity.recorded) != 2 || activity.recorded[0] != "v2" || activity.recorded[1] != "v3" {
		t.Fatalf("viewed=%#v want [v2 v3]", activity.recorded)
	}
	if len(searcher.calls) != 2 || searcher.calls[1] != "next" {
		t.Fatalf("search cursors=%#v", searcher.calls)
	}
}

func TestActivityMaintainRevisitsVacanciesOutsideTheWindow(t *testing.T) {
	now := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	activity := &maintainActivity{Repository: repository, records: []core.ProfileActivityRecord{
		{ProfileID: "primary", Kind: core.ProfileActivityVacancyInspected, SourceID: "v1", OccurredAt: now.Add(-maintainInspectedWindow - time.Hour)},
	}}
	searcher := &maintainSearcher{pages: map[string]core.SearchPage{
		"": {Vacancies: []core.Vacancy{{Platform: "hh", ExternalID: "v1", State: core.VacancyStateOpen}}, Done: true},
	}}
	handler := maintainHandler(t, now, searcher, activity)
	if err := handler.Handle(context.Background(), maintainTask(t, 1)); err != nil {
		t.Fatalf("handle maintain: %v", err)
	}
	if len(activity.recorded) != 1 || activity.recorded[0] != "v1" {
		t.Fatalf("viewed=%#v want [v1]", activity.recorded)
	}
}
