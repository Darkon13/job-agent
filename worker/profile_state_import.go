package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// ProfileImportVacancyStore keeps the vacancies the import discovers.
type ProfileImportVacancyStore interface {
	UpsertVacancy(ctx context.Context, vacancy core.Vacancy) (bool, error)
}

// ProfileImportApplicationStore resolves and creates application records.
type ProfileImportApplicationStore interface {
	Application(ctx context.Context, key core.ApplicationKey) (core.Application, error)
	CreateApplication(ctx context.Context, candidate core.Application) (core.Application, bool, error)
}

// ProfileImportStateStore stores the observed platform state of an application.
type ProfileImportStateStore interface {
	SaveApplicationPlatformState(ctx context.Context, state core.ApplicationPlatformState) error
}

// ProfileImportIDGenerator creates the identifiers of the follow-up tasks.
type ProfileImportIDGenerator interface {
	NewID(prefix string) (string, error)
}

// ProfileImportIdentityStore stores the account summary the import captures.
type ProfileImportIdentityStore interface {
	SaveProfileIdentity(ctx context.Context, profileID core.ProfileID, identity core.ProfileIdentity, now time.Time) error
}

// ProfileImportIdentityReader builds the reader of the account summary for one
// profile and its browser state file.
type ProfileImportIdentityReader func(profileID core.ProfileID, stateFile string) (adapter.ProfileIdentityReader, error)

// ProfileImportHandler loads the whole platform account state into the local
// database. Every negotiation becomes an application record with its observed
// state, including responses that were sent outside this service, and the full
// conversation catalog is discovered afterwards. The platform is never changed:
// the import only reads.
type ProfileImportHandler struct {
	vacancies    ProfileImportVacancyStore
	applications ProfileImportApplicationStore
	states       ProfileImportStateStore
	observers    *ApplicationStateObserverRegistry
	tasks        broker.TaskStore
	ids          ProfileImportIDGenerator
	clock        Clock
	identities   ProfileImportIdentityStore
	identityFor  ProfileImportIdentityReader
	stateFiles   map[core.ProfileID]string
}

// ConfigureIdentity attaches the account summary capture: the import then also
// refreshes the identity of the profile it loads.
func (handler *ProfileImportHandler) ConfigureIdentity(store ProfileImportIdentityStore, reader ProfileImportIdentityReader, stateFiles map[core.ProfileID]string) {
	if handler == nil {
		return
	}
	handler.identities = store
	handler.identityFor = reader
	handler.stateFiles = stateFiles
}

func NewProfileImportHandler(
	vacancies ProfileImportVacancyStore,
	applications ProfileImportApplicationStore,
	states ProfileImportStateStore,
	observers *ApplicationStateObserverRegistry,
	tasks broker.TaskStore,
	ids ProfileImportIDGenerator,
	clock Clock,
) (*ProfileImportHandler, error) {
	if vacancies == nil || applications == nil || states == nil || observers == nil || tasks == nil || ids == nil || clock == nil {
		return nil, errors.New("profile import handler requires stores, observers, task store, id generator and clock")
	}
	return &ProfileImportHandler{
		vacancies: vacancies, applications: applications, states: states,
		observers: observers, tasks: tasks, ids: ids, clock: clock,
	}, nil
}

func (handler *ProfileImportHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ProfileImportPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode profile import payload: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("profile import task profile does not match payload")
	}
	observer, err := handler.observers.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	observed, err := observer.ObserveApplicationStates(ctx, payload.ProfileID)
	if err != nil {
		return err
	}
	if err := validateApplicationObservation(observed, handler.clock.Now()); err != nil {
		return err
	}
	imported, updated := 0, 0
	for _, item := range observed.Applications {
		key := core.ApplicationKey{
			ProfileID: payload.ProfileID,
			Vacancy:   core.VacancyKey{Platform: task.Platform, ExternalID: item.ExternalVacancyID},
		}
		// The negotiations list carries identifiers only: the title arrives
		// later from a search or a conversation observation of the vacancy.
		if _, err := handler.vacancies.UpsertVacancy(ctx, core.Vacancy{
			Platform: task.Platform, ExternalID: item.ExternalVacancyID,
			State: core.VacancyStateOpen, ObservedAt: observed.ObservedAt,
		}); err != nil {
			return err
		}
		application, err := handler.applications.Application(ctx, key)
		if errors.Is(err, storage.ErrApplicationNotFound) {
			application, err = handler.importApplication(ctx, key, observed.ObservedAt)
			if err != nil {
				return err
			}
			imported++
		} else if err != nil {
			return err
		} else {
			updated++
		}
		if err := handler.states.SaveApplicationPlatformState(ctx, observationState(application.ID, item, observed.ObservedAt)); err != nil {
			if errors.Is(err, storage.ErrApplicationRemoved) {
				continue
			}
			return err
		}
	}
	if err := handler.captureIdentity(ctx, payload.ProfileID); err != nil {
		// The identity is a convenience: a failed capture must not fail the
		// import that already loaded the applications.
		slog.Default().Warn("profile import identity capture failed", "profile", payload.ProfileID, "error", err)
	}
	discovery, err := handler.enqueueFullDiscovery(ctx, payload.ProfileID, task.Platform)
	if err != nil {
		return err
	}
	slog.Default().Info("profile import finished",
		"profile", payload.ProfileID, "negotiations", len(observed.Applications),
		"applications_imported", imported, "applications_updated", updated, "discovery_created", discovery)
	return nil
}

// importApplication records a response that already exists on the platform.
// The local lifecycle marks it submitted and documents the origin through the
// already_applied decision, so counters and dedup stay honest.
func (handler *ProfileImportHandler) importApplication(ctx context.Context, key core.ApplicationKey, now time.Time) (core.Application, error) {
	id, err := handler.ids.NewID("application")
	if err != nil {
		return core.Application{}, err
	}
	application, err := core.NewApplication(core.ApplicationID(id), key, now)
	if err != nil {
		return core.Application{}, err
	}
	if err := application.Transition(core.ApplicationPreparing, now); err != nil {
		return core.Application{}, err
	}
	if err := application.RecordPreparation("already_applied", "отклик уже существует на платформе", "", "", now); err != nil {
		return core.Application{}, err
	}
	for _, status := range []core.ApplicationStatus{core.ApplicationReady, core.ApplicationSubmitting, core.ApplicationSubmitted} {
		if err := application.Transition(status, now); err != nil {
			return core.Application{}, err
		}
	}
	stored, _, err := handler.applications.CreateApplication(ctx, application)
	return stored, err
}

// captureIdentity refreshes the stored account summary through the same
// browser session the import already uses.
func (handler *ProfileImportHandler) captureIdentity(ctx context.Context, profileID core.ProfileID) error {
	if handler.identities == nil || handler.identityFor == nil {
		return nil
	}
	stateFile := strings.TrimSpace(handler.stateFiles[profileID])
	if stateFile == "" {
		return nil
	}
	reader, err := handler.identityFor(profileID, stateFile)
	if err != nil {
		return err
	}
	snapshot, err := reader.ReadProfileIdentity(ctx, profileID)
	if err != nil {
		return err
	}
	identity := core.ProfileIdentity{
		DisplayName: strings.TrimSpace(snapshot.DisplayName),
		Email:       strings.TrimSpace(snapshot.Email),
		Phone:       strings.TrimSpace(snapshot.Phone),
		AccountHash: strings.TrimSpace(snapshot.AccountHash),
		CapturedAt:  snapshot.CapturedAt,
	}
	if identity.DisplayName == "" && identity.Email == "" && identity.Phone == "" && identity.AccountHash == "" {
		return nil
	}
	return handler.identities.SaveProfileIdentity(ctx, profileID, identity, handler.clock.Now())
}

// enqueueFullDiscovery starts a complete conversation catalog pass: the import
// must load every chat, not only the recent-activity window of the frequent
// poll. The key is unique per run so a later import re-reads the catalog.
func (handler *ProfileImportHandler) enqueueFullDiscovery(ctx context.Context, profileID core.ProfileID, platform core.Platform) (bool, error) {
	payload, err := json.Marshal(core.ConversationDiscoverPayload{ProfileID: profileID})
	if err != nil {
		return false, fmt.Errorf("encode profile import discovery payload: %w", err)
	}
	taskID, err := handler.ids.NewID("task")
	if err != nil {
		return false, err
	}
	correlationID, err := handler.ids.NewID("correlation")
	if err != nil {
		return false, err
	}
	now := handler.clock.Now()
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(taskID), Type: core.TaskConversationDiscover,
		IdempotencyKey: "profile.import.discovery:" + string(profileID) + ":" + strconv.FormatInt(now.UnixNano(), 10),
		Source:         "profile.import", Platform: platform, ProfileID: profileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
	}, now)
	if err != nil {
		return false, err
	}
	return handler.tasks.Enqueue(ctx, task)
}
