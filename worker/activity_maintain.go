package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ActivityMaintainHandler opens real candidate vacancies through the profile
// browser session so the applicant activity stays warm. It only reads: the
// apply decision still belongs to the application pipeline, and closed
// vacancies fall through to the submit flow.
type ActivityMaintainHandler struct {
	applications interface {
		ListApplications(ctx context.Context, filter storage.ApplicationFilter) ([]core.Application, error)
	}
	vacancies  storage.VacancyRepository
	transports *ApplicationTransportRegistry
	activity   storage.ProfileActivityRepository
	snapshots  storage.ProfileActivitySnapshotRepository
	clock      Clock
}

func NewActivityMaintainHandler(
	applications interface {
		ListApplications(ctx context.Context, filter storage.ApplicationFilter) ([]core.Application, error)
	},
	vacancies storage.VacancyRepository,
	transports *ApplicationTransportRegistry,
	activity storage.ProfileActivityRepository,
	snapshots storage.ProfileActivitySnapshotRepository,
	clock Clock,
) (*ActivityMaintainHandler, error) {
	if applications == nil || vacancies == nil || transports == nil || activity == nil || snapshots == nil || clock == nil {
		return nil, errors.New("activity maintain handler requires applications, vacancies, transports, activity, snapshots and clock")
	}
	return &ActivityMaintainHandler{applications: applications, vacancies: vacancies, transports: transports, activity: activity, snapshots: snapshots, clock: clock}, nil
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
	if handler.activityAtMaximum(ctx, payload.ProfileID) {
		// Nothing to top up: the score is already at the platform maximum.
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
	return nil
}

func (handler *ActivityMaintainHandler) activityAtMaximum(ctx context.Context, profileID core.ProfileID) bool {
	snapshots, err := handler.snapshots.ListProfileActivitySnapshots(ctx, storage.ProfileActivitySnapshotFilter{
		ProfileID: profileID, Limit: 1,
	})
	if err != nil || len(snapshots) == 0 {
		return false
	}
	score := snapshots[0].Score
	return score != nil && *score >= 100
}

// targets returns lazy vacancy views: either fresh global search results or
// the profile application queue when no search query was configured.
func (handler *ActivityMaintainHandler) targets(ctx context.Context, payload core.ProfileActivityMaintainPayload) ([]func() (core.Vacancy, error), error) {
	reader, err := handler.transports.ResolveVacancyReader(payload.ProfileID)
	if err != nil {
		return nil, err
	}
	targets := make([]func() (core.Vacancy, error), 0, payload.Count)
	if len(payload.Query) > 0 {
		searcher, err := handler.transports.ResolveVacancySearcher(payload.ProfileID)
		if err != nil {
			return nil, err
		}
		page, err := searcher.Search(ctx, payload.ProfileID, payload.Query, "")
		if err != nil {
			return nil, err
		}
		for _, vacancy := range page.Vacancies {
			vacancy := vacancy
			if vacancy.State != core.VacancyStateOpen {
				continue
			}
			targets = append(targets, func() (core.Vacancy, error) {
				return reader.ReadVacancy(ctx, payload.ProfileID, vacancy.Key())
			})
		}
		return targets, nil
	}
	applications, err := handler.applications.ListApplications(ctx, storage.ApplicationFilter{
		ProfileID: payload.ProfileID, Limit: 200,
	})
	if err != nil {
		return nil, err
	}
	for _, application := range applications {
		switch application.Status {
		case core.ApplicationNew, core.ApplicationPreparing, core.ApplicationReady:
		default:
			continue
		}
		application := application
		targets = append(targets, func() (core.Vacancy, error) {
			return reader.ReadVacancy(ctx, application.Key.ProfileID, application.Key.Vacancy)
		})
	}
	return targets, nil
}
