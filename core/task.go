package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type TaskType string

const (
	TaskVacancySearchPage          TaskType = "vacancy.search_page"
	TaskApplicationCampaign        TaskType = "application.campaign"
	TaskApplicationSubmit          TaskType = "application.submit"
	TaskApplicationRemove          TaskType = "application.remove"
	TaskApplicationRetention       TaskType = "application.retention"
	TaskApplicationStateSync       TaskType = "application.state.sync"
	TaskQuestionnaireAnswer        TaskType = "questionnaire.answer"
	TaskTestComplete               TaskType = "test.complete"
	TaskTestCapture                TaskType = "test.capture"
	TaskReviewAnswer               TaskType = "review.answer"
	TaskConversationReply          TaskType = "conversation.reply"
	TaskConversationSend           TaskType = "conversation.send"
	TaskConversationFollowUp       TaskType = "conversation.follow_up"
	TaskConversationFollowUpSelect TaskType = "conversation.follow_up.select"
	TaskConversationDiscover       TaskType = "conversation.discover"
	TaskConversationMarkRead       TaskType = "conversation.mark_read"
	TaskConversationSync           TaskType = "conversation.sync"
	TaskVacancyInspect             TaskType = "vacancy.inspect"
	TaskResumePublish              TaskType = "resume.publish"
	TaskResumeTouch                TaskType = "resume.touch"
	TaskResumeUpdate               TaskType = "resume.update"
	TaskProfileActivityObserve     TaskType = "profile.activity.observe"
	TaskProfileSessionRefresh      TaskType = "profile.session_refresh"
	TaskProfileBootstrap           TaskType = "profile.bootstrap"
	TaskProfileStateReconcile      TaskType = "profile_state.reconcile"
	TaskProfileStateApply          TaskType = "profile_state.apply"
	TaskSkillVerificationStart     TaskType = "skill_verification.start"
	TaskSkillVerificationSync      TaskType = "skill_verification.sync"
	TaskCalendarFindSlots          TaskType = "calendar.find_slots"
	TaskCalendarCreateEvent        TaskType = "calendar.create_event"
	TaskChallengeRespond           TaskType = "challenge.respond"
	TaskNotificationDeliver        TaskType = "notification.deliver"
)

type TaskStatus string

const (
	TaskNew                 TaskStatus = "new"
	TaskProcessing          TaskStatus = "processing"
	TaskWaitingConfirmation TaskStatus = "waiting_confirmation"
	TaskRetryScheduled      TaskStatus = "retry_scheduled"
	TaskCompleted           TaskStatus = "completed"
	TaskFailed              TaskStatus = "failed"
	TaskDismissed           TaskStatus = "dismissed"
)

// TaskPriority controls dispatch order only among tasks claimed by the same
// consumer. A higher value is claimed first; equal priorities preserve the
// existing available_at/created_at FIFO order. Priority never preempts a task
// that is already processing.
type TaskPriority int

const (
	TaskPriorityMin    TaskPriority = -1000
	TaskPriorityNormal TaskPriority = 0
	TaskPriorityMax    TaskPriority = 1000

	// TaskPriorityProfileStateApply makes desired-state mutations visible as
	// system work. Cross-type ordering is enforced separately during claim.
	TaskPriorityProfileStateApply TaskPriority = TaskPriorityMax
)

func (priority TaskPriority) Validate() error {
	if priority < TaskPriorityMin || priority > TaskPriorityMax {
		return fmt.Errorf("task priority must be between %d and %d", TaskPriorityMin, TaskPriorityMax)
	}
	return nil
}

type TaskFailure struct {
	Category ErrorCategory `json:"category"`
	Message  string        `json:"message,omitempty"`
}

type Task struct {
	ID             TaskID          `json:"id"`
	Type           TaskType        `json:"type"`
	Status         TaskStatus      `json:"status"`
	IdempotencyKey string          `json:"idempotency_key"`
	Source         string          `json:"source"`
	Platform       Platform        `json:"platform,omitempty"`
	ProfileID      ProfileID       `json:"profile_id,omitempty"`
	CorrelationID  CorrelationID   `json:"correlation_id"`
	Payload        json.RawMessage `json:"payload"`
	Priority       TaskPriority    `json:"priority"`
	Attempts       int             `json:"attempts"`
	AvailableAt    time.Time       `json:"available_at"`
	Deadline       *time.Time      `json:"deadline,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Failure        *TaskFailure    `json:"failure,omitempty"`
}

type NewTaskParams struct {
	ID             TaskID
	Type           TaskType
	IdempotencyKey string
	Source         string
	Platform       Platform
	ProfileID      ProfileID
	CorrelationID  CorrelationID
	Payload        json.RawMessage
	Priority       TaskPriority
	AvailableAt    time.Time
	Deadline       *time.Time
}

func NewTask(params NewTaskParams, now time.Time) (Task, error) {
	if params.ID == "" || params.Type == "" || params.IdempotencyKey == "" || params.Source == "" || params.CorrelationID == "" {
		return Task{}, errors.New("task requires id, type, idempotency key, source and correlation id")
	}
	if now.IsZero() {
		return Task{}, errors.New("task requires current time")
	}
	if len(params.Payload) == 0 || !json.Valid(params.Payload) {
		return Task{}, errors.New("task requires valid JSON payload")
	}
	if err := params.Priority.Validate(); err != nil {
		return Task{}, err
	}
	availableAt := params.AvailableAt
	if availableAt.IsZero() {
		availableAt = now
	}
	if params.Deadline != nil && !params.Deadline.After(now) {
		return Task{}, errors.New("task deadline must be in the future")
	}
	if params.Deadline != nil && !availableAt.Before(*params.Deadline) {
		return Task{}, errors.New("task must become available before its deadline")
	}
	var deadline *time.Time
	if params.Deadline != nil {
		value := *params.Deadline
		deadline = &value
	}
	return Task{
		ID: params.ID, Type: params.Type, Status: TaskNew,
		IdempotencyKey: params.IdempotencyKey, Source: params.Source,
		Platform: params.Platform, ProfileID: params.ProfileID,
		CorrelationID: params.CorrelationID, Payload: append(json.RawMessage(nil), params.Payload...),
		Priority: params.Priority, AvailableAt: availableAt, Deadline: deadline, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (task *Task) Transition(to TaskStatus, now time.Time) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if now.IsZero() || now.Before(task.UpdatedAt) {
		return errors.New("task transition time must not move backwards")
	}
	if !taskTransitionAllowed(task.Status, to) {
		return fmt.Errorf("task transition %s -> %s is not allowed", task.Status, to)
	}
	if to == TaskProcessing {
		if now.Before(task.AvailableAt) {
			return fmt.Errorf("task is unavailable until %s", task.AvailableAt.Format(time.RFC3339Nano))
		}
		if task.Deadline != nil && !now.Before(*task.Deadline) {
			return errors.New("task deadline has expired")
		}
		task.Attempts++
	}
	task.Status = to
	task.UpdatedAt = now
	if to != TaskFailed {
		task.Failure = nil
	}
	return nil
}

func (task *Task) ScheduleRetry(retryAt time.Time, operationError *OperationError, now time.Time) error {
	if operationError == nil {
		return errors.New("retry requires operation error")
	}
	if err := operationError.Validate(); err != nil {
		return err
	}
	switch operationError.Category {
	case ErrorTemporaryFailure, ErrorRateLimited, ErrorQuotaExceeded,
		ErrorUnauthorized, ErrorValidationRequired, ErrorConfirmationRequired, ErrorAmbiguousResult:
	default:
		return fmt.Errorf("error category %q is not retryable", operationError.Category)
	}
	if !retryAt.After(now) {
		return errors.New("retry time must be in the future")
	}
	if task.Deadline != nil && !retryAt.Before(*task.Deadline) {
		return errors.New("retry time must be before task deadline")
	}
	if err := task.Transition(TaskRetryScheduled, now); err != nil {
		return err
	}
	task.AvailableAt = retryAt
	task.Failure = &TaskFailure{Category: operationError.Category, Message: operationError.Message}
	return nil
}

func (task *Task) Fail(operationError *OperationError, now time.Time) error {
	if operationError == nil {
		return errors.New("task failure requires operation error")
	}
	if err := operationError.Validate(); err != nil {
		return err
	}
	if err := task.Transition(TaskFailed, now); err != nil {
		return err
	}
	task.Failure = &TaskFailure{Category: operationError.Category, Message: operationError.Message}
	return nil
}

// RestartFailed explicitly reopens a terminal failure. It resets the bounded
// worker-attempt counter because the operator has started a new retry cycle.
func (task *Task) RestartFailed(now time.Time) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if task.Status != TaskFailed {
		return errors.New("only a failed task can be restarted")
	}
	if now.IsZero() || now.Before(task.UpdatedAt) {
		return errors.New("task restart time must not move backwards")
	}
	if task.Deadline != nil && !now.Before(*task.Deadline) {
		return errors.New("task deadline has expired")
	}
	task.Status = TaskNew
	task.Attempts = 0
	task.AvailableAt = now
	task.UpdatedAt = now
	task.Failure = nil
	return nil
}

// DismissFailure records an explicit operator decision to stop retrying a
// failed task. The original failure is retained for diagnostics.
func (task *Task) DismissFailure(now time.Time) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if task.Status != TaskFailed {
		return errors.New("only a failed task can be dismissed")
	}
	if now.IsZero() || now.Before(task.UpdatedAt) {
		return errors.New("task dismissal time must not move backwards")
	}
	task.Status = TaskDismissed
	task.UpdatedAt = now
	return nil
}

func taskTransitionAllowed(from, to TaskStatus) bool {
	allowed := map[TaskStatus]map[TaskStatus]struct{}{
		TaskNew: {
			TaskProcessing: {}, TaskFailed: {},
		},
		TaskProcessing: {
			TaskWaitingConfirmation: {}, TaskRetryScheduled: {}, TaskCompleted: {}, TaskFailed: {},
		},
		TaskWaitingConfirmation: {
			TaskProcessing: {}, TaskFailed: {},
		},
		TaskRetryScheduled: {
			TaskProcessing: {}, TaskFailed: {},
		},
	}
	_, exists := allowed[from][to]
	return exists
}
