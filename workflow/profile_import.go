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

// ProfileImportWorkflow creates a durable command that loads the platform
// account state into the local database: all negotiations become application
// records and the full conversation catalog is discovered.
type ProfileImportWorkflow struct {
	tasks     broker.TaskStore
	clock     Clock
	ids       IDGenerator
	platforms map[core.ProfileID]core.Platform
}

func NewProfileImportWorkflow(tasks broker.TaskStore, clock Clock, ids IDGenerator, platforms map[core.ProfileID]core.Platform) (*ProfileImportWorkflow, error) {
	if tasks == nil || clock == nil || ids == nil {
		return nil, errors.New("profile import workflow requires task store, clock and id generator")
	}
	copied := make(map[core.ProfileID]core.Platform, len(platforms))
	for profileID, platform := range platforms {
		if profileID == "" || platform == "" {
			return nil, errors.New("profile import platforms require profile and platform")
		}
		copied[profileID] = platform
	}
	return &ProfileImportWorkflow{tasks: tasks, clock: clock, ids: ids, platforms: copied}, nil
}

// Available reports whether the profile can import: it must be declared with a
// platform and have a browser session.
func (workflow *ProfileImportWorkflow) Available(profileID core.ProfileID) bool {
	if workflow == nil {
		return false
	}
	_, exists := workflow.platforms[profileID]
	return exists
}

func (workflow *ProfileImportWorkflow) Enqueue(ctx context.Context, profileID core.ProfileID, source, requestKey string) (core.Task, bool, error) {
	if workflow == nil {
		return core.Task{}, false, errors.New("profile import workflow is nil")
	}
	platform, exists := workflow.platforms[profileID]
	if !exists {
		return core.Task{}, false, fmt.Errorf("profile import is not available for %q", profileID)
	}
	source = strings.TrimSpace(source)
	requestKey = strings.TrimSpace(requestKey)
	if source == "" || requestKey == "" {
		return core.Task{}, false, errors.New("profile import requires source and request key")
	}
	payload, err := json.Marshal(core.ProfileImportPayload{ProfileID: profileID})
	if err != nil {
		return core.Task{}, false, fmt.Errorf("encode profile import payload: %w", err)
	}
	taskID, err := workflow.ids.NewID("task")
	if err != nil {
		return core.Task{}, false, err
	}
	correlationID, err := workflow.ids.NewID("correlation")
	if err != nil {
		return core.Task{}, false, err
	}
	now := workflow.clock.Now()
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(taskID), Type: core.TaskProfileStateImport,
		IdempotencyKey: "profile.import:" + string(profileID) + ":" + requestKey,
		Source:         source, Platform: platform, ProfileID: profileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: payload,
	}, now)
	if err != nil {
		return core.Task{}, false, err
	}
	created, err := workflow.tasks.Enqueue(ctx, task)
	if err != nil {
		return core.Task{}, false, err
	}
	return task, created, nil
}
