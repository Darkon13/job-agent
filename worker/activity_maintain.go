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
	clock      Clock
}

func NewActivityMaintainHandler(
	applications interface {
		ListApplications(ctx context.Context, filter storage.ApplicationFilter) ([]core.Application, error)
	},
	vacancies storage.VacancyRepository,
	transports *ApplicationTransportRegistry,
	activity storage.ProfileActivityRepository,
	clock Clock,
) (*ActivityMaintainHandler, error) {
	if applications == nil || vacancies == nil || transports == nil || activity == nil || clock == nil {
		return nil, errors.New("activity maintain handler requires applications, vacancies, transports, activity and clock")
	}
	return &ActivityMaintainHandler{applications: applications, vacancies: vacancies, transports: transports, activity: activity, clock: clock}, nil
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
	reader, err := handler.transports.ResolveVacancyReader(payload.ProfileID)
	if err != nil {
		return err
	}
	applications, err := handler.applications.ListApplications(ctx, storage.ApplicationFilter{
		ProfileID: payload.ProfileID, Limit: 200,
	})
	if err != nil {
		return err
	}
	viewed := 0
	for _, application := range applications {
		if viewed >= payload.Count {
			break
		}
		switch application.Status {
		case core.ApplicationNew, core.ApplicationPreparing, core.ApplicationReady:
		default:
			continue
		}
		vacancy, err := reader.ReadVacancy(ctx, application.Key.ProfileID, application.Key.Vacancy)
		if err != nil {
			// A closed or unavailable vacancy is not an activity failure; the
			// submit flow will classify it.
			continue
		}
		if vacancy.Key() != application.Key.Vacancy || vacancy.Validate() != nil {
			continue
		}
		if _, err := handler.vacancies.UpsertVacancy(ctx, vacancy); err != nil {
			continue
		}
		if err := recordProfileActivity(ctx, handler.activity, vacancy.Platform, application.Key.ProfileID, "",
			core.ProfileActivityVacancyInspected, string(application.ID), handler.clock.Now()); err != nil {
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
