package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	_, created, err := workflow.enqueue(ctx, profileID, platform, core.TaskTestCapture, key, source, payload)
	return created, err
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
	_, created, err := workflow.enqueue(ctx, profileID, platform, core.TaskQuestionnaireAnswer, key, "vacancy-test-chain", payload)
	return created, err
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
	_, created, err := workflow.enqueue(ctx, profileID, platform, core.TaskTestComplete, key, "vacancy-test-chain", payload)
	return created, err
}

// EnqueueReviewAnswer records a human selection for the current review prompt.
// The task is keyed by session, prompt, revision and the client request key, so
// a retried submission never duplicates the selection.
func (workflow *VacancyTestWorkflow) EnqueueReviewAnswer(ctx context.Context, session core.ReviewSession, payload core.ReviewAnswerPayload, requestKey string) (core.Task, bool, error) {
	if err := session.Validate(); err != nil {
		return core.Task{}, false, err
	}
	if err := payload.Validate(); err != nil {
		return core.Task{}, false, err
	}
	if payload.SessionID != session.ID {
		return core.Task{}, false, errors.New("review answer session mismatch")
	}
	requestKey = strings.TrimSpace(requestKey)
	if requestKey == "" {
		return core.Task{}, false, errors.New("review answer requires request key")
	}
	key, err := core.ReviewAnswerIdempotencyKey(payload.SessionID, payload.PromptID, payload.ExpectedRevision)
	if err != nil {
		return core.Task{}, false, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return core.Task{}, false, fmt.Errorf("encode review answer task: %w", err)
	}
	return workflow.enqueue(ctx, session.ProfileID, session.Platform, core.TaskReviewAnswer, key+":"+requestKey, "review-answer", encoded)
}

func (workflow *VacancyTestWorkflow) enqueue(ctx context.Context, profileID core.ProfileID, platform core.Platform, taskType core.TaskType, key, source string, payload []byte) (core.Task, bool, error) {
	id, err := workflow.ids.NewID("task")
	if err != nil {
		return core.Task{}, false, err
	}
	correlationID, err := workflow.ids.NewID("correlation")
	if err != nil {
		return core.Task{}, false, err
	}
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(id), Type: taskType, IdempotencyKey: key, Source: source,
		Platform: platform, ProfileID: profileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
	}, workflow.clock.Now())
	if err != nil {
		return core.Task{}, false, err
	}
	created, err := workflow.tasks.Enqueue(ctx, task)
	if err != nil || created {
		return task, created, err
	}
	existing, err := workflow.tasks.TaskByIdempotencyKey(ctx, key)
	if err != nil {
		return core.Task{}, false, fmt.Errorf("load idempotent vacancy test task: %w", err)
	}
	return existing, false, nil
}
