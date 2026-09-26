package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type validationReader struct {
	vacancies map[string]core.Vacancy
	errs      map[string]error
}

func (reader validationReader) ReadVacancy(_ context.Context, _ core.ProfileID, key core.VacancyKey) (core.Vacancy, error) {
	if err := reader.errs[key.ExternalID]; err != nil {
		return core.Vacancy{}, err
	}
	vacancy, exists := reader.vacancies[key.ExternalID]
	if !exists {
		return core.Vacancy{}, &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "vacancies.read.browser", Platform: "hh",
			Message: "HH vacancy is closed or unavailable", Metadata: map[string]string{"code": "vacancy_closed"},
		}
	}
	return vacancy, nil
}

func validationApplication(t *testing.T, repository *storagememory.Repository, id, vacancyID string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	vacancy := core.Vacancy{Platform: "hh", ExternalID: vacancyID, Title: "Go developer", State: core.VacancyStateOpen, ObservedAt: now.Add(-4 * time.Hour)}
	if _, err := repository.UpsertVacancy(ctx, vacancy); err != nil {
		t.Fatalf("store vacancy: %v", err)
	}
	application, err := core.NewApplication(core.ApplicationID(id), core.ApplicationKey{ProfileID: "primary", Vacancy: vacancy.Key()}, now.Add(-4*time.Hour))
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	if _, _, err := repository.CreateApplication(ctx, application); err != nil {
		t.Fatalf("create application: %v", err)
	}
	if err := application.Transition(core.ApplicationPreparing, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("preparing: %v", err)
	}
	if err := repository.SaveApplication(ctx, application, core.ApplicationNew); err != nil {
		t.Fatalf("save preparing: %v", err)
	}
	application.DecisionCode = "questionnaire_required"
	if err := application.Transition(core.ApplicationWaitingValidation, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("waiting validation: %v", err)
	}
	if err := repository.SaveApplication(ctx, application, core.ApplicationPreparing); err != nil {
		t.Fatalf("save waiting validation: %v", err)
	}
}

func TestValidationRefreshSkipsClosedVacancies(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	validationApplication(t, repository, "application-closed", "vacancy-closed", now)
	validationApplication(t, repository, "application-open", "vacancy-open", now)

	transports := NewApplicationTransportRegistry()
	if err := transports.RegisterVacancyReader("primary", validationReader{
		vacancies: map[string]core.Vacancy{
			"vacancy-open": {Platform: "hh", ExternalID: "vacancy-open", Title: "Go developer", State: core.VacancyStateOpen, ObservedAt: now},
		},
		errs: map[string]error{},
	}); err != nil {
		t.Fatalf("register reader: %v", err)
	}
	handler, err := NewValidationRefreshHandler(repository, repository, transports, &conversationClock{now: now})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	payload, _ := json.Marshal(core.ApplicationValidationRefreshPayload{
		ProfileID: "primary", Count: 10, MinAge: core.Duration(time.Hour),
	})
	task := core.Task{ID: "validation-refresh-1", Type: core.TaskApplicationValidationCheck, ProfileID: "primary", Platform: "hh", Payload: payload}
	if err := handler.Handle(context.Background(), task); err != nil {
		t.Fatalf("handle refresh: %v", err)
	}
	closed, err := repository.ApplicationByID(context.Background(), "application-closed")
	if err != nil || closed.Status != core.ApplicationSkipped || closed.DecisionCode != "vacancy_closed" {
		t.Fatalf("closed application=%#v err=%v", closed, err)
	}
	open, err := repository.ApplicationByID(context.Background(), "application-open")
	if err != nil || open.Status != core.ApplicationWaitingValidation {
		t.Fatalf("open application=%#v err=%v", open, err)
	}
}
