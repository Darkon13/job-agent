package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

var ErrJobRunNotFound = errors.New("runnable job not found")

// JobRunDefinition is the transport-neutral command behind one configured job.
// Trigger timing is deliberately absent: a manual API run becomes available
// immediately while retaining the same typed payload and priority as cron.
type JobRunDefinition struct {
	Tag       string
	TaskType  core.TaskType
	Platform  core.Platform
	ProfileID core.ProfileID
	Payload   json.RawMessage
	Priority  core.TaskPriority
}

type JobRunDescriptor struct {
	Tag       string            `json:"tag"`
	TaskType  core.TaskType     `json:"task_type"`
	Platform  core.Platform     `json:"platform"`
	ProfileID core.ProfileID    `json:"profile_id"`
	Priority  core.TaskPriority `json:"priority"`
}

type JobRunWorkflow struct {
	tasks       broker.TaskStore
	clock       Clock
	ids         IDGenerator
	definitions map[string]JobRunDefinition
	descriptors []JobRunDescriptor
}

func NewJobRunWorkflow(tasks broker.TaskStore, clock Clock, ids IDGenerator, definitions []JobRunDefinition) (*JobRunWorkflow, error) {
	if tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("job run workflow requires task store, clock and id generator")
	}
	workflow := &JobRunWorkflow{
		tasks: tasks, clock: clock, ids: ids,
		definitions: make(map[string]JobRunDefinition, len(definitions)),
	}
	for _, definition := range definitions {
		if err := definition.validate(); err != nil {
			return nil, err
		}
		if existing, exists := workflow.definitions[definition.Tag]; exists {
			if !sameJobRunDefinition(existing, definition) {
				return nil, fmt.Errorf("job %q has conflicting runnable commands", definition.Tag)
			}
			continue
		}
		definition.Payload = append(json.RawMessage(nil), definition.Payload...)
		workflow.definitions[definition.Tag] = definition
		workflow.descriptors = append(workflow.descriptors, JobRunDescriptor{
			Tag: definition.Tag, TaskType: definition.TaskType, Platform: definition.Platform,
			ProfileID: definition.ProfileID, Priority: definition.Priority,
		})
	}
	sort.Slice(workflow.descriptors, func(i, j int) bool { return workflow.descriptors[i].Tag < workflow.descriptors[j].Tag })
	return workflow, nil
}

func (workflow *JobRunWorkflow) Definitions() []JobRunDescriptor {
	if workflow == nil {
		return nil
	}
	return append([]JobRunDescriptor(nil), workflow.descriptors...)
}

func (workflow *JobRunWorkflow) Run(ctx context.Context, tag, requestKey string) (core.Task, bool, error) {
	if workflow == nil {
		return core.Task{}, false, errors.New("job run requires workflow")
	}
	tag = strings.TrimSpace(tag)
	requestKey = strings.TrimSpace(requestKey)
	if tag == "" || requestKey == "" {
		return core.Task{}, false, errors.New("job run requires tag and idempotency key")
	}
	definition, exists := workflow.definitions[tag]
	if !exists {
		return core.Task{}, false, fmt.Errorf("%w: %s", ErrJobRunNotFound, tag)
	}
	taskID, err := workflow.ids.NewID("task")
	if err != nil {
		return core.Task{}, false, err
	}
	correlationID, err := workflow.ids.NewID("correlation")
	if err != nil {
		return core.Task{}, false, err
	}
	idempotencyKey := jobRunIdempotencyKey(tag, requestKey)
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(taskID), Type: definition.TaskType, IdempotencyKey: idempotencyKey,
		Source: "job-api:" + tag, Platform: definition.Platform, ProfileID: definition.ProfileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: definition.Payload,
		Priority: definition.Priority,
	}, workflow.clock.Now())
	if err != nil {
		return core.Task{}, false, err
	}
	created, err := workflow.tasks.Enqueue(ctx, task)
	if err != nil || created {
		return task, created, err
	}
	existing, err := workflow.tasks.TaskByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return core.Task{}, false, fmt.Errorf("load idempotent job run: %w", err)
	}
	return existing, false, nil
}

func (definition JobRunDefinition) validate() error {
	if strings.TrimSpace(definition.Tag) == "" || definition.TaskType == "" || definition.Platform == "" || definition.ProfileID == "" || len(definition.Payload) == 0 || !json.Valid(definition.Payload) {
		return fmt.Errorf("runnable job %q is incomplete", definition.Tag)
	}
	if err := definition.Priority.Validate(); err != nil {
		return fmt.Errorf("runnable job %q: %w", definition.Tag, err)
	}
	return nil
}

func sameJobRunDefinition(left, right JobRunDefinition) bool {
	return left.Tag == right.Tag && left.TaskType == right.TaskType && left.Platform == right.Platform &&
		left.ProfileID == right.ProfileID && left.Priority == right.Priority && bytes.Equal(left.Payload, right.Payload)
}

func jobRunIdempotencyKey(tag, requestKey string) string {
	digest := sha256.Sum256([]byte(tag + "\x00" + requestKey))
	return "job.run:" + hex.EncodeToString(digest[:])
}
