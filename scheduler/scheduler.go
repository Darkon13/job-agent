package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Darkon13/job-agent/broker"
	"github.com/Darkon13/job-agent/core"
	"github.com/robfig/cron/v3"
)

type Definition struct {
	JobTag       string
	TriggerIndex int
	Expression   string
	Timezone     string
	ActionType   core.TaskType
	Platform     core.Platform
	ProfileID    core.ProfileID
	Payload      json.RawMessage
	Priority     core.TaskPriority
	JitterMin    time.Duration
	JitterMax    time.Duration
}

type Entry struct {
	Definition
	NextRunAt time.Time
}

type Store interface {
	SyncSchedules(ctx context.Context, entries []Entry, now time.Time) error
	DueSchedules(ctx context.Context, now time.Time, limit int) ([]Entry, error)
	HasActiveScheduledTask(ctx context.Context, jobTag string) (bool, error)
	AdvanceSchedule(ctx context.Context, jobTag string, triggerIndex int, expected, next, now time.Time) (bool, error)
}

type Clock interface{ Now() time.Time }
type IDGenerator interface {
	NewID(prefix string) (string, error)
}

type Scheduler struct {
	store Store
	queue broker.TaskQueue
	clock Clock
	ids   IDGenerator
}

func New(store Store, queue broker.TaskQueue, clock Clock, ids IDGenerator) (*Scheduler, error) {
	if store == nil || queue == nil || clock == nil || ids == nil {
		return nil, errors.New("scheduler requires store, queue, clock and id generator")
	}
	return &Scheduler{store: store, queue: queue, clock: clock, ids: ids}, nil
}

func (scheduler *Scheduler) Sync(ctx context.Context, definitions []Definition) error {
	now := scheduler.clock.Now()
	entries := make([]Entry, 0, len(definitions))
	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return err
		}
		schedule, err := parseSchedule(definition)
		if err != nil {
			return err
		}
		entries = append(entries, Entry{Definition: definition, NextRunAt: schedule.Next(now)})
	}
	return scheduler.store.SyncSchedules(ctx, entries, now)
}

func (scheduler *Scheduler) ReconcileDue(ctx context.Context) (int, error) {
	now := scheduler.clock.Now()
	entries, err := scheduler.store.DueSchedules(ctx, now, 100)
	if err != nil {
		return 0, err
	}
	advanced := 0
	for _, entry := range entries {
		schedule, err := parseSchedule(entry.Definition)
		if err != nil {
			return advanced, err
		}
		next := schedule.Next(now)
		active, err := scheduler.store.HasActiveScheduledTask(ctx, entry.JobTag)
		if err != nil {
			return advanced, err
		}
		if !active {
			if err := scheduler.enqueue(ctx, entry, now); err != nil {
				return advanced, err
			}
		}
		changed, err := scheduler.store.AdvanceSchedule(ctx, entry.JobTag, entry.TriggerIndex, entry.NextRunAt, next, now)
		if err != nil {
			return advanced, err
		}
		if changed {
			advanced++
		}
	}
	return advanced, nil
}

func (scheduler *Scheduler) enqueue(ctx context.Context, entry Entry, now time.Time) error {
	taskID, err := scheduler.ids.NewID("task")
	if err != nil {
		return err
	}
	correlationID, err := scheduler.ids.NewID("correlation")
	if err != nil {
		return err
	}
	key := scheduleIdempotencyKey(entry.JobTag, entry.TriggerIndex, entry.NextRunAt)
	availableAt := entry.NextRunAt.Add(deterministicJitter(key, entry.JitterMin, entry.JitterMax))
	task, err := core.NewTask(core.NewTaskParams{
		ID: core.TaskID(taskID), Type: entry.ActionType, IdempotencyKey: key,
		Source: "cron:" + entry.JobTag, Platform: entry.Platform, ProfileID: entry.ProfileID,
		CorrelationID: core.CorrelationID(correlationID), Payload: entry.Payload,
		Priority: entry.Priority, AvailableAt: availableAt,
	}, now)
	if err != nil {
		return err
	}
	if _, err := scheduler.queue.Enqueue(ctx, task); err != nil {
		return fmt.Errorf("enqueue scheduled job %s: %w", entry.JobTag, err)
	}
	return nil
}

func (definition Definition) Validate() error {
	if definition.JobTag == "" || definition.TriggerIndex < 0 || definition.Expression == "" || definition.Timezone == "" ||
		definition.ActionType == "" || definition.Platform == "" || definition.ProfileID == "" || len(definition.Payload) == 0 || !json.Valid(definition.Payload) {
		return errors.New("scheduled job definition is incomplete")
	}
	if definition.JitterMin < 0 || definition.JitterMax < definition.JitterMin {
		return errors.New("scheduled job has invalid jitter bounds")
	}
	if err := definition.Priority.Validate(); err != nil {
		return fmt.Errorf("scheduled job %s: %w", definition.JobTag, err)
	}
	_, err := parseSchedule(definition)
	return err
}

func parseSchedule(definition Definition) (cron.Schedule, error) {
	if _, err := time.LoadLocation(definition.Timezone); err != nil {
		return nil, fmt.Errorf("job %s timezone: %w", definition.JobTag, err)
	}
	schedule, err := cron.ParseStandard("CRON_TZ=" + definition.Timezone + " " + definition.Expression)
	if err != nil {
		return nil, fmt.Errorf("job %s cron: %w", definition.JobTag, err)
	}
	return schedule, nil
}

func scheduleIdempotencyKey(tag string, triggerIndex int, scheduledAt time.Time) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", tag, triggerIndex, scheduledAt.UTC().UnixNano())))
	return "job.cron:" + hex.EncodeToString(digest[:])
}

func deterministicJitter(key string, minimum, maximum time.Duration) time.Duration {
	if maximum <= minimum {
		return minimum
	}
	digest := sha256.Sum256([]byte(key))
	span := uint64(maximum-minimum) + 1
	return minimum + time.Duration(binary.BigEndian.Uint64(digest[:8])%span)
}
