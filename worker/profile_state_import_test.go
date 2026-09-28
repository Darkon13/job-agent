package worker

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	brokermemory "github.com/Darkon13/job-agent/broker/memory"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type importVacancyStore struct{ count int }

func (store *importVacancyStore) UpsertVacancy(context.Context, core.Vacancy) (bool, error) {
	store.count++
	return true, nil
}

type importApplicationStore struct {
	existing map[string]core.Application
	created  []core.Application
}

func (store *importApplicationStore) Application(_ context.Context, key core.ApplicationKey) (core.Application, error) {
	if application, exists := store.existing[key.Vacancy.ExternalID]; exists {
		return application, nil
	}
	return core.Application{}, storage.ErrApplicationNotFound
}

func (store *importApplicationStore) CreateApplication(_ context.Context, candidate core.Application) (core.Application, bool, error) {
	store.created = append(store.created, candidate)
	return candidate, true, nil
}

type importStateStore struct {
	saved []core.ApplicationPlatformState
}

func (store *importStateStore) SaveApplicationPlatformState(_ context.Context, state core.ApplicationPlatformState) error {
	store.saved = append(store.saved, state)
	return nil
}

type importObserver struct {
	result adapter.ApplicationStateObservationResult
}

func (observer importObserver) ObserveApplicationStates(context.Context, core.ProfileID) (adapter.ApplicationStateObservationResult, error) {
	return observer.result, nil
}

type importIDs struct{ next int }

func (ids *importIDs) NewID(prefix string) (string, error) {
	ids.next++
	return prefix + "-" + strconv.Itoa(ids.next), nil
}

func TestProfileImportCreatesMissingApplicationsAndStartsFullDiscovery(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	observers := NewApplicationStateObserverRegistry()
	observed := adapter.ApplicationStateObservationResult{
		ObservedAt: now,
		Applications: []adapter.ApplicationStateObservation{
			{ExternalNegotiationID: "n-1", ExternalVacancyID: "42", PlatformState: "RESPONSE", Disposition: core.ApplicationDispositionPending},
			{ExternalNegotiationID: "n-2", ExternalVacancyID: "43", PlatformState: "DISCARD", Disposition: core.ApplicationDispositionRejected},
		},
	}
	if err := observers.Register("main", importObserver{result: observed}); err != nil {
		t.Fatalf("register observer: %v", err)
	}
	known := core.Application{
		ID: "application-known", Key: core.ApplicationKey{ProfileID: "main", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "43"}},
		Status: core.ApplicationSubmitted,
	}
	vacancies := &importVacancyStore{}
	applications := &importApplicationStore{existing: map[string]core.Application{"43": known}}
	states := &importStateStore{}
	queue := brokermemory.NewQueue()
	handler, err := NewProfileImportHandler(vacancies, applications, states, observers, queue, &importIDs{}, fixedClock{now})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	payload, err := json.Marshal(core.ProfileImportPayload{ProfileID: "main"})
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: "task-import", Type: core.TaskProfileStateImport, IdempotencyKey: "key-import",
		Source: "dashboard-import", Platform: "hh", ProfileID: "main", CorrelationID: "corr-import", Payload: payload,
	}, now)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if err := handler.Handle(ctx, task); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(applications.created) != 1 {
		t.Fatalf("imported applications = %#v", applications.created)
	}
	imported := applications.created[0]
	if imported.Status != core.ApplicationSubmitted || imported.DecisionCode != "already_applied" || imported.Key.Vacancy.ExternalID != "42" {
		t.Fatalf("imported application = %#v", imported)
	}
	if vacancies.count != 2 || len(states.saved) != 2 {
		t.Fatalf("vacancies=%d states=%d", vacancies.count, len(states.saved))
	}
	tasks := queue.Tasks()
	if len(tasks) != 1 || tasks[0].Type != core.TaskConversationDiscover || tasks[0].ProfileID != "main" {
		t.Fatalf("follow-up tasks = %#v", tasks)
	}
	var discovery core.ConversationDiscoverPayload
	if err := json.Unmarshal(tasks[0].Payload, &discovery); err != nil {
		t.Fatalf("decode discovery payload: %v", err)
	}
	if discovery.ProfileID != "main" || discovery.MaxPages != 0 {
		t.Fatalf("discovery payload = %#v", discovery)
	}
}
