package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/Darkon13/job-agent/config"
	"github.com/Darkon13/job-agent/core"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

func TestRunEnqueuesResumeTouchIdempotently(t *testing.T) {
	directory := t.TempDir()
	configPath, databasePath := triggerConfig(t, directory)
	now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
	var output bytes.Buffer
	args := []string{"-idempotency-key", "manual-touch-1", configPath, "touch-primary"}
	if err := run(context.Background(), args, &output, now); err != nil {
		t.Fatalf("first trigger: %v", err)
	}
	if err := run(context.Background(), args, &output, now.Add(time.Minute)); err != nil {
		t.Fatalf("second trigger: %v", err)
	}
	if !strings.Contains(output.String(), "created=true") || !strings.Contains(output.String(), "created=false") {
		t.Fatalf("unexpected output:\n%s", output.String())
	}
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	task, err := store.TaskByIdempotencyKey(context.Background(), "manual-touch-1")
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	var payload core.ResumeTouchPayload
	if task.Type != core.TaskResumeTouch || task.Priority != 140 || json.Unmarshal(task.Payload, &payload) != nil || payload.ResumeID != "resume-1" {
		t.Fatalf("unexpected task: %#v payload=%#v", task, payload)
	}
}

func TestRunRequiresExplicitIdempotencyKey(t *testing.T) {
	var output bytes.Buffer
	err := run(context.Background(), []string{"config.json", "touch-primary"}, &output, time.Now())
	if err == nil || !strings.Contains(err.Error(), "idempotency-key") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunEnqueuesProfileStateReconcile(t *testing.T) {
	directory := t.TempDir()
	configPath, databasePath := triggerConfig(t, directory)
	var output bytes.Buffer
	if err := run(context.Background(), []string{
		"-idempotency-key", "manual-reconcile-1", configPath, "reconcile-about",
	}, &output, time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("trigger reconcile: %v", err)
	}
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	task, err := store.TaskByIdempotencyKey(context.Background(), "manual-reconcile-1")
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	var payload core.ProfileStateReconcilePayload
	if task.Type != core.TaskProfileStateReconcile || json.Unmarshal(task.Payload, &payload) != nil || payload.ResourceTag != "primary-about" {
		t.Fatalf("unexpected task: %#v payload=%#v", task, payload)
	}
}

func TestRunEnqueuesProfileActivityObservation(t *testing.T) {
	directory := t.TempDir()
	configPath, databasePath := triggerConfig(t, directory)
	var output bytes.Buffer
	if err := run(context.Background(), []string{
		"-idempotency-key", "manual-observe-1", configPath, "observe-primary",
	}, &output, time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("trigger activity observation: %v", err)
	}
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	task, err := store.TaskByIdempotencyKey(context.Background(), "manual-observe-1")
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	var payload core.ProfileActivityObservePayload
	if task.Type != core.TaskProfileActivityObserve || json.Unmarshal(task.Payload, &payload) != nil || payload.ResumeID != "resume-1" {
		t.Fatalf("unexpected task: %#v payload=%#v", task, payload)
	}
}

func TestRunEnqueuesConversationFollowUpSelection(t *testing.T) {
	directory := t.TempDir()
	configPath, databasePath := triggerConfig(t, directory)
	var output bytes.Buffer
	if err := run(context.Background(), []string{
		"-idempotency-key", "manual-follow-up-selection-1", configPath, "remind-oldest",
	}, &output, time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("trigger follow-up selection: %v", err)
	}
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	task, err := store.TaskByIdempotencyKey(context.Background(), "manual-follow-up-selection-1")
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	var payload core.ConversationFollowUpSelectPayload
	if task.Type != core.TaskConversationFollowUpSelect || json.Unmarshal(task.Payload, &payload) != nil || payload.Strategy != core.FollowUpSelectOldestUnanswered {
		t.Fatalf("unexpected task: %#v payload=%#v", task, payload)
	}
}

func TestRunEnqueuesConversationDiscovery(t *testing.T) {
	directory := t.TempDir()
	configPath, databasePath := triggerConfig(t, directory)
	var output bytes.Buffer
	if err := run(context.Background(), []string{
		"-idempotency-key", "manual-conversation-sync-1", configPath, "sync-conversations",
	}, &output, time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("trigger conversation sync: %v", err)
	}
	store, err := storesqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	task, err := store.TaskByIdempotencyKey(context.Background(), "manual-conversation-sync-1")
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	var payload core.ConversationDiscoverPayload
	if task.Type != core.TaskConversationDiscover || json.Unmarshal(task.Payload, &payload) != nil || payload.ProfileID != "primary" {
		t.Fatalf("unexpected task: %#v payload=%#v", task, payload)
	}
}

func triggerConfig(t *testing.T, directory string) (string, string) {
	t.Helper()
	databasePath := filepath.Join(directory, "job-agent.db")
	if err := storesqlite.MigrateUp(databasePath); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	cfg := appconfig.Config{
		Database: appconfig.DatabaseConfig{Driver: "sqlite", Path: databasePath},
		Adapters: []appconfig.AdapterConfig{{Tag: "hh-main", Type: "hh"}},
		Profiles: []appconfig.Profile{{
			Tag: "primary", Adapter: "hh-main", Resume: "resume-1", Enabled: true,
			Conversations: appconfig.ConversationPolicy{AllowSend: true},
		}},
		Resources: []appconfig.ProfileStateResourceConfig{{
			Tag: "primary-about", Type: appconfig.ResourceTypeProfileState, Profile: "primary",
			Ownership: core.ProfileStateOwnershipDeclaredFields,
			State:     json.RawMessage(`{"resumes":{"resume-1":{"about":"Backend"}}}`),
		}},
		Jobs: []appconfig.Job{
			{
				Tag: "sync-conversations", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
				Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "*/10 * * * *", Timezone: "UTC", Misfire: "run_once"}},
				Action:   appconfig.JobAction{Type: appconfig.JobActionConversationSync, Profile: "primary"},
			},
			{
				Tag: "touch-primary", Enabled: true, Priority: 140, Concurrency: appconfig.JobConcurrencyForbid,
				Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "0 * * * *", Timezone: "UTC", Misfire: "run_once"}},
				Action:   appconfig.JobAction{Type: appconfig.JobActionResumeTouch, Profile: "primary"},
			},
			{
				Tag: "observe-primary", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
				Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "*/30 * * * *", Timezone: "UTC", Misfire: "run_once"}},
				Action:   appconfig.JobAction{Type: appconfig.JobActionProfileActivityObserve, Profile: "primary"},
			},
			{
				Tag: "remind-oldest", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
				Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "15 11 * * 1-5", Timezone: "UTC", Misfire: "run_once"}},
				Action: appconfig.JobAction{
					Type: appconfig.JobActionConversationFollowUpSelect, Profile: "primary",
					FollowUp: &appconfig.ConversationFollowUpSelectionConfig{
						Strategy: core.FollowUpSelectOldestUnanswered, MinimumSilence: core.Duration(72 * time.Hour),
						RunAfter: core.Duration(time.Minute), DeadlineAfter: core.Duration(24 * time.Hour),
						Content: core.MessageContent{Text: "Подскажите, вакансия ещё актуальна?"},
						Policy: core.FollowUpPolicy{
							CancelOnIncoming: true, RequireActiveConversation: true,
							MaxFollowUps: 1, Cooldown: core.Duration(72 * time.Hour),
						},
					},
				},
			},
			{
				Tag: "reconcile-about", Enabled: true, Concurrency: appconfig.JobConcurrencyForbid,
				Triggers: []appconfig.JobTrigger{{Type: "cron", Expression: "0 9 * * *", Timezone: "UTC", Misfire: "run_once"}},
				Action:   appconfig.JobAction{Type: appconfig.JobActionProfileStateReconcile, Resource: "primary-about"},
			},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath, databasePath
}
