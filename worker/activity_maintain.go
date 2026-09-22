package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ActivityMaintainHandler opens real candidate vacancies through the profile
// browser session so the applicant activity stays warm. It only reads: the
// apply decision still belongs to the application pipeline, and closed
// vacancies fall through to the submit flow.
type ActivityMaintainHandler struct {
	vacancies  storage.VacancyRepository
	transports *ApplicationTransportRegistry
	activity   storage.ProfileActivityRepository
	snapshots  storage.ProfileActivitySnapshotRepository
	clock      Clock
}

func NewActivityMaintainHandler(
	vacancies storage.VacancyRepository,
	transports *ApplicationTransportRegistry,
	activity storage.ProfileActivityRepository,
	snapshots storage.ProfileActivitySnapshotRepository,
	clock Clock,
) (*ActivityMaintainHandler, error) {
	if vacancies == nil || transports == nil || activity == nil || snapshots == nil || clock == nil {
		return nil, errors.New("activity maintain handler requires vacancies, transports, activity, snapshots and clock")
	}
	return &ActivityMaintainHandler{vacancies: vacancies, transports: transports, activity: activity, snapshots: snapshots, clock: clock}, nil
}

func (handler *ActivityMaintainHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ProfileActivityMaintainPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode profile activity maintain payload: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("profile activity maintain task profile does not match payload")
	}
	score := handler.activityScore(ctx, payload.ProfileID)
	if score != nil && *score >= 100 {
		// Nothing to top up: the score is already at the platform maximum.
		slog.Default().Info("activity maintenance skipped: score is already at the platform maximum",
			"profile", payload.ProfileID, "score", *score)
		return nil
	}
	targets, err := handler.targets(ctx, payload)
	if err != nil {
		return err
	}
	viewed := 0
	for _, target := range targets {
		if viewed >= payload.Count {
			break
		}
		vacancy, err := target()
		if err != nil {
			continue
		}
		if _, err := handler.vacancies.UpsertVacancy(ctx, vacancy); err != nil {
			continue
		}
		if err := recordProfileActivity(ctx, handler.activity, vacancy.Platform, payload.ProfileID, "",
			core.ProfileActivityVacancyInspected, vacancy.ExternalID, handler.clock.Now()); err != nil {
			return err
		}
		viewed++
		if viewed < payload.Count && payload.Pause.Value() > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(payload.Pause.Value()):
			}
		}
	}
	attributes := []any{"profile", payload.ProfileID, "viewed", viewed, "candidates", len(targets)}
	if score != nil {
		attributes = append(attributes, "score", *score)
	}
	slog.Default().Info("activity maintenance finished", attributes...)
	return nil
}

// activityScore returns the latest observed activity score, or nil when the
// profile has no snapshot yet.
func (handler *ActivityMaintainHandler) activityScore(ctx context.Context, profileID core.ProfileID) *int {
	snapshots, err := handler.snapshots.ListProfileActivitySnapshots(ctx, storage.ProfileActivitySnapshotFilter{
		ProfileID: profileID, Limit: 1,
	})
	if err != nil || len(snapshots) == 0 {
		return nil
	}
	return snapshots[0].Score
}

// targets returns lazy vacancy views from the configured global search. The
// queue is intentionally not used: activity grows from opening vacancies the
// profile has not inspected yet, so recently viewed cards are skipped.
func (handler *ActivityMaintainHandler) targets(ctx context.Context, payload core.ProfileActivityMaintainPayload) ([]func() (core.Vacancy, error), error) {
	if len(payload.Query) == 0 {
		return nil, nil
	}
	reader, err := handler.transports.ResolveVacancyReader(payload.ProfileID)
	if err != nil {
		return nil, err
	}
	searcher, err := handler.transports.ResolveVacancySearcher(payload.ProfileID)
	if err != nil {
		return nil, err
	}
	inspected, err := handler.inspectedVacancies(ctx, payload.ProfileID)
	if err != nil {
		return nil, err
	}
	page, err := searcher.Search(ctx, payload.ProfileID, payload.Query, "")
	if err != nil {
		return nil, err
	}
	targets := make([]func() (core.Vacancy, error), 0, payload.Count)
	for _, vacancy := range page.Vacancies {
		if vacancy.State != core.VacancyStateOpen {
			continue
		}
		if _, seen := inspected[vacancy.ExternalID]; seen {
			continue
		}
		vacancy := vacancy
		targets = append(targets, func() (core.Vacancy, error) {
			return reader.ReadVacancy(ctx, payload.ProfileID, vacancy.Key())
		})
	}
	return targets, nil
}

// inspectedVacancies lists vacancy identities already opened by the maintain
// job so repeated runs move to fresh cards instead of re-opening the same ones.
func (handler *ActivityMaintainHandler) inspectedVacancies(ctx context.Context, profileID core.ProfileID) (map[string]struct{}, error) {
	records, err := handler.activity.ListProfileActivity(ctx, storage.ProfileActivityFilter{
		ProfileID: profileID, Kind: core.ProfileActivityVacancyInspected,
	})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		seen[record.SourceID] = struct{}{}
	}
	return seen, nil
}
