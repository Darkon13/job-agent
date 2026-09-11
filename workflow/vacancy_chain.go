package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

// VacancyTestWorkflow enqueues the durable steps of the vacancy test answer
// chain. Producers stay free of transport details; every step is an idempotent
// task that a type-filtered worker claims separately.
type VacancyTestWorkflow struct {
	tasks broker.TaskStore
	clock Clock
	ids   IDGenerator
}

func NewVacancyTestWorkflow(tasks broker.TaskStore, clock Clock, ids IDGenerator) (*VacancyTestWorkflow, error) {
	if tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("vacancy test workflow requires task store, clock and id generator")
	}
	return &VacancyTestWorkflow{tasks: tasks, clock: clock, ids: ids}, nil
}

func (workflow *VacancyTestWorkflow) EnqueueCapture(ctx context.Context, profileID core.ProfileID, platform core.Platform, externalID string, source string) (bool, error) {
	if source == "" {
		return false, errors.New("vacancy test capture requires source")
	}
	key, err := core.TestCaptureIdempotencyKey(platform, externalID, profileID)
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(core.TestCapturePayload{ProfileID: profileID, Platform: platform, VacancyExternalID: externalID})
	if err != nil {
		return false, fmt.Errorf("encode test capture task: %w", err)
	}
	return workflow.enqueue(ctx, profileID, platform, core.TaskTestCapture, key, source, payload)
}

func (workflow *VacancyTestWorkflow) EnqueueAnswer(ctx context.Context, profileID core.ProfileID, platform core.Platform, externalID string, answers []core.ResolvedAnswer, requestKey string) (bool, error) {
	key, err := core.QuestionnaireAnswerIdempotencyKey(platform, externalID, profileID, requestKey)
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(core.QuestionnaireAnswerPayload{
		ProfileID: profileID, Platform: platform, VacancyExternalID: externalID, Answers: answers,
	})
	if err != nil {
		return false, fmt.Errorf("encode questionnaire answer task: %w", err)
	}
	return workflow.enqueue(ctx, profileID, platform, core.TaskQuestionnaireAnswer, key, "vacancy-test-chain", payload)
}

func (workflow *VacancyTestWorkflow) EnqueueComplete(ctx context.Context, profileID core.ProfileID, platform core.Platform, externalID string, status core.TestAttemptStatus, fingerprint, requestKey string) (bool, error) {
	key, err := core.TestCompleteIdempotencyKey(platform, externalID, profileID, requestKey)
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(core.TestCompletePayload{
		ProfileID: profileID, Platform: platform, VacancyExternalID: externalID,
		Status: status, AttemptFingerprint: fingerprint,
	})
	if err != nil {
		return false, fmt.Errorf("encode test complete task: %w", err)
	}
	return workflow.enqueue(ctx, profileID, platform, core.TaskTestComplete, key, "vacancy-test-chain", payload)
}

func (workflow *VacancyTestWorkflow) enqueue(ctx context.Context, profileID core.ProfileID, platform core.Platform, taskType core.TaskType, key, source string, payload []byte) (bool, error) {
	id, err := workflow.ids.NewID("task")
	if err != nil {
		return false, err
	}
	correlationID, err := workflow.ids.NewID("correlation")
	if err != nil {
		return false, err
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(id), Type: taskType, IdempotencyKey: key, Source: source,
		Platform: platform, ProfileID: profileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
	}, workflow.clock.Now())
	if err != nil {
		return false, err
	}
	return workflow.tasks.Enqueue(ctx, task)
}
