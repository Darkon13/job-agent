package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
)

var ErrJobRunNotFound = errors.New("runnable job not found")

// JobRunCommand is one durable task behind a configured job. A job that names
// several profiles with the same schedule keeps one tag but carries one command
// per profile, so a manual run triggers all of them.
type JobRunCommand struct {
	TaskType  core.TaskType
	Platform  core.Platform
	ProfileID core.ProfileID
	Payload   json.RawMessage
	Priority  core.TaskPriority
}

// JobRunDefinition is the transport-neutral command set behind one configured
// job. Trigger timing is deliberately absent: a manual API run becomes
// available immediately while retaining the same typed payloads and priorities
// as cron.
type JobRunDefinition struct {
	Tag      string
	Commands []JobRunCommand
}

// JobSchedule describes one configured trigger of a runnable job. NextRunAt is
// the cron time before jitter; the actual enqueue happens within the jitter
// window that follows.
type JobSchedule struct {
	TriggerIndex int       `json:"trigger_index"`
	Expression   string    `json:"expression"`
	Timezone     string    `json:"timezone"`
	NextRunAt    time.Time `json:"next_run_at"`
	JitterMin    string    `json:"jitter_min,omitempty"`
	JitterMax    string    `json:"jitter_max,omitempty"`
}

type JobRunDescriptor struct {
	Tag       string            `json:"tag"`
	TaskType  core.TaskType     `json:"task_type"`
	Platform  core.Platform     `json:"platform"`
	ProfileID core.ProfileID    `json:"profile_id"`
	Profiles  []core.ProfileID  `json:"profiles,omitempty"`
	Priority  core.TaskPriority `json:"priority"`
	Payload   json.RawMessage   `json:"payload,omitempty"`
	Schedules []JobSchedule     `json:"schedules,omitempty"`
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
		copied := JobRunDefinition{Tag: definition.Tag, Commands: cloneJobRunCommands(definition.Commands)}
		workflow.definitions[copied.Tag] = copied
		workflow.descriptors = append(workflow.descriptors, copied.descriptor())
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
	var first core.Task
	anyCreated := false
	for index, command := range definition.Commands {
		taskID, err := workflow.ids.NewID("task")
		if err != nil {
			return core.Task{}, false, err
		}
		correlationID, err := workflow.ids.NewID("correlation")
		if err != nil {
			return core.Task{}, false, err
		}
		idempotencyKey := jobRunIdempotencyKey(tag, requestKey, index)
		task, err := core.NewTask(core.NewTaskParams{
			ID: core.TaskID(taskID), Type: command.TaskType, IdempotencyKey: idempotencyKey,
			Source: "job-api:" + tag, Platform: command.Platform, ProfileID: command.ProfileID,
			CorrelationID: core.CorrelationID(correlationID), Payload: command.Payload,
			Priority: command.Priority,
		}, workflow.clock.Now())
		if err != nil {
			return core.Task{}, false, err
		}
		created, err := workflow.tasks.Enqueue(ctx, task)
		if err != nil {
			return core.Task{}, false, fmt.Errorf("enqueue job %s command %d: %w", tag, index, err)
		}
		if !created {
			task, err = workflow.tasks.TaskByIdempotencyKey(ctx, idempotencyKey)
			if err != nil {
				return core.Task{}, false, fmt.Errorf("load idempotent job run: %w", err)
			}
		}
		if index == 0 {
			first = task
		}
		anyCreated = anyCreated || created
	}
	return first, anyCreated, nil
}

func (definition JobRunDefinition) descriptor() JobRunDescriptor {
	first := definition.Commands[0]
	profiles := make([]core.ProfileID, 0, len(definition.Commands))
	seen := make(map[core.ProfileID]struct{}, len(definition.Commands))
	for _, command := range definition.Commands {
		if _, exists := seen[command.ProfileID]; exists {
			continue
		}
		seen[command.ProfileID] = struct{}{}
		profiles = append(profiles, command.ProfileID)
	}
	descriptor := JobRunDescriptor{
		Tag: definition.Tag, TaskType: first.TaskType, Platform: first.Platform,
		ProfileID: first.ProfileID, Priority: first.Priority,
		Payload: append(json.RawMessage(nil), first.Payload...),
	}
	if len(profiles) > 1 {
		descriptor.Profiles = profiles
	}
	return descriptor
}

func (definition JobRunDefinition) validate() error {
	if strings.TrimSpace(definition.Tag) == "" || len(definition.Commands) == 0 {
		return fmt.Errorf("runnable job %q is incomplete", definition.Tag)
	}
	taskType := definition.Commands[0].TaskType
	for index, command := range definition.Commands {
		if command.TaskType == "" || command.Platform == "" || command.ProfileID == "" ||
			len(command.Payload) == 0 || !json.Valid(command.Payload) {
			return fmt.Errorf("runnable job %q command %d is incomplete", definition.Tag, index)
		}
		if command.TaskType != taskType {
			return fmt.Errorf("runnable job %q mixes command types", definition.Tag)
		}
		if err := command.Priority.Validate(); err != nil {
			return fmt.Errorf("runnable job %q command %d: %w", definition.Tag, index, err)
		}
	}
	return nil
}

func (command JobRunCommand) copy() JobRunCommand {
	command.Payload = append(json.RawMessage(nil), command.Payload...)
	return command
}

func cloneJobRunCommands(commands []JobRunCommand) []JobRunCommand {
	cloned := make([]JobRunCommand, 0, len(commands))
	for _, command := range commands {
		cloned = append(cloned, command.copy())
	}
	return cloned
}

func jobRunIdempotencyKey(tag, requestKey string, commandIndex int) string {
	digest := sha256.Sum256([]byte(tag + "\x00" + requestKey + "\x00" + strconv.Itoa(commandIndex)))
	return "job.run:" + hex.EncodeToString(digest[:])
}
