package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

// TestCompleteHandler records the platform outcome of a finished vacancy test.
// It never submits answers and never re-runs the attempt.
type TestCompleteHandler struct {
	attempts storage.TestAttemptRepository
	clock    Clock
}

func NewTestCompleteHandler(attempts storage.TestAttemptRepository, clock Clock) (*TestCompleteHandler, error) {
	if attempts == nil || clock == nil {
		return nil, errors.New("test complete handler requires attempts repository and clock")
	}
	return &TestCompleteHandler{attempts: attempts, clock: clock}, nil
}

func (handler *TestCompleteHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.TestCompletePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode test complete task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("test complete task profile mismatch")
	}
	if _, err := handler.record(ctx, payload); err != nil {
		return err
	}
	return nil
}

func (handler *TestCompleteHandler) record(ctx context.Context, payload core.TestCompletePayload) (core.TestAttempt, error) {
	now := handler.clock.Now()
	attempts := uint64(1)
	latest, found, err := handler.attempts.LatestTestAttempt(ctx, payload.Platform, payload.ProfileID, payload.VacancyExternalID)
	if err != nil {
		return core.TestAttempt{}, err
	}
	if found {
		if latest.Status == payload.Status && latest.AttemptFingerprint == payload.AttemptFingerprint {
			return latest, nil
		}
		attempts = latest.Attempts + 1
	}
	attempt := core.TestAttempt{
		Platform: payload.Platform, ProfileID: payload.ProfileID, ExternalID: payload.VacancyExternalID,
		TestDefinitionID: core.VacancyTestDefinitionID(payload.Platform, payload.VacancyExternalID),
		Status:           payload.Status, AttemptFingerprint: payload.AttemptFingerprint,
		Attempts: attempts, ObservedAt: now, UpdatedAt: now,
	}
	if err := handler.attempts.SaveTestAttempt(ctx, attempt); err != nil {
		return core.TestAttempt{}, err
	}
	return attempt, nil
}
