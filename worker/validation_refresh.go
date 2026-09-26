package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ValidationRefreshHandler rechecks the vacancy of questionnaires and tests
// that never became applications. A vacancy that closed or aged out turns the
// application into a skip, so "Нужно участие" never collects dead cards.
type ValidationRefreshHandler struct {
	states       storage.ApplicationPlatformStateRepository
	applications storage.ApplicationRepository
	transports   *ApplicationTransportRegistry
	clock        Clock
}

func NewValidationRefreshHandler(
	states storage.ApplicationPlatformStateRepository,
	applications storage.ApplicationRepository,
	transports *ApplicationTransportRegistry,
	clock Clock,
) (*ValidationRefreshHandler, error) {
	if states == nil || applications == nil || transports == nil || clock == nil {
		return nil, errors.New("validation refresh handler requires states, applications, transports and clock")
	}
	return &ValidationRefreshHandler{states: states, applications: applications, transports: transports, clock: clock}, nil
}

func (handler *ValidationRefreshHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ApplicationValidationRefreshPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode validation refresh payload: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("validation refresh task profile does not match payload")
	}
	reader, err := handler.transports.ResolveVacancyReader(payload.ProfileID)
	if err != nil {
		return err
	}
	applications, err := handler.states.ListStaleValidationApplications(ctx, payload.ProfileID)
	if err != nil {
		return err
	}
	sort.Slice(applications, func(i, j int) bool {
		if !applications[i].UpdatedAt.Equal(applications[j].UpdatedAt) {
			return applications[i].UpdatedAt.Before(applications[j].UpdatedAt)
		}
		return applications[i].ID < applications[j].ID
	})
	now := handler.clock.Now()
	cutoff := now
	if payload.MinAge.Value() > 0 {
		cutoff = now.Add(-payload.MinAge.Value())
	}
	checked, skipped := 0, 0
	for _, application := range applications {
		if checked >= payload.Count {
			break
		}
		if application.Key.Vacancy.Platform != task.Platform || application.UpdatedAt.After(cutoff) {
			continue
		}
		if application.Status == core.ApplicationSkipped {
			continue
		}
		checked++
		vacancy, err := reader.ReadVacancy(ctx, payload.ProfileID, application.Key.Vacancy)
		if err != nil {
			if vacancyUnavailable(err) {
				if skipErr := handler.skip(ctx, application, "HH сообщил, что вакансия закрыта или недоступна", now); skipErr != nil {
					return skipErr
				}
				skipped++
			}
			continue
		}
		if vacancy.State != core.VacancyStateOpen || vacancyAttributeBool(vacancy, "closed_for_applicants") {
			if err := handler.skip(ctx, application, "вакансия закрыта для отклика", now); err != nil {
				return err
			}
			skipped++
		}
	}
	slog.Default().Info("application validation refresh finished",
		"profile", payload.ProfileID, "checked", checked, "skipped", skipped, "candidates", len(applications))
	return nil
}

func (handler *ValidationRefreshHandler) skip(ctx context.Context, application core.Application, reason string, now time.Time) error {
	previous := application.Status
	application.DecisionCode = "vacancy_closed"
	application.DecisionReason = reason
	if err := application.Transition(core.ApplicationSkipped, now); err != nil {
		return err
	}
	return handler.applications.SaveApplication(ctx, application, previous)
}

// vacancyUnavailable reports the permanent "not accessible" read failure.
func vacancyUnavailable(err error) bool {
	var operationError *core.OperationError
	return errors.As(err, &operationError) && operationError.Validate() == nil &&
		(operationError.Metadata["code"] == "vacancy_closed" || operationError.Category == core.ErrorPermanentFailure)
}
