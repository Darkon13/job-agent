package workflow

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/scheduler"
	"github.com/Darkon13/job-agent/storage"
)

// AuthPauseReason marks the pauses this guard creates, so a later successful
// sign-in resumes exactly them and never touches operator pauses.
const AuthPauseReason = "нет сессии HH: требуется вход"

// authRecoveryTaskLimit bounds one post-login recovery burst. The oldest
// failures are retried first, and the next sign-in continues the rest.
const authRecoveryTaskLimit = 100

// AuthPauseStore is the part of the job pause store the guard uses.
type AuthPauseStore interface {
	PauseJob(ctx context.Context, jobTag string, profileID core.ProfileID, reason string, now time.Time) error
	ResumeJob(ctx context.Context, jobTag string, profileID core.ProfileID) (int, error)
	JobPauses(ctx context.Context) ([]scheduler.Pause, error)
}

// AuthFailedTaskStore lists the tasks that failed because the profile had no
// usable session.
type AuthFailedTaskStore interface {
	FailedAuthTasks(ctx context.Context, profileID core.ProfileID, limit int) ([]storage.FailedTaskSummary, error)
}

// AuthGuard reacts to authorization failures and to the sign-in that resolves
// them. A missing platform session cannot heal by itself: without the guard a
// scheduled job keeps creating tasks that fail one after another, and after the
// operator signs in the recorded failures stay failed until they are retried by
// hand.
type AuthGuard struct {
	pauses  AuthPauseStore
	tasks   AuthFailedTaskStore
	control *TaskControlWorkflow
	clock   Clock
}

func NewAuthGuard(pauses AuthPauseStore, tasks AuthFailedTaskStore, control *TaskControlWorkflow, clock Clock) (*AuthGuard, error) {
	if pauses == nil || tasks == nil || control == nil || clock == nil {
		return nil, errors.New("auth guard requires pause store, failed task store, task control and clock")
	}
	return &AuthGuard{pauses: pauses, tasks: tasks, control: control, clock: clock}, nil
}

// Guard pauses the job that produced an unauthorized task. Other jobs of the
// profile pause on their own first failure, so the account stops working
// instead of failing on every occurrence.
func (guard *AuthGuard) Guard(ctx context.Context, task core.Task) error {
	tag := jobTagFromSource(task.Source)
	if tag == "" || task.ProfileID == "" {
		return nil
	}
	if err := guard.pauses.PauseJob(ctx, tag, task.ProfileID, AuthPauseReason, guard.clock.Now()); err != nil {
		return err
	}
	slog.Default().Warn("job paused after an authorization failure",
		"job", tag, "profile", task.ProfileID, "task", task.ID)
	return nil
}

// AuthCompleted resumes the jobs the guard paused and retries the failures that
// happened without a session. It is attached to the auth completion hook, so a
// successful sign-in returns the account to work by itself.
func (guard *AuthGuard) AuthCompleted(ctx context.Context, profileID core.ProfileID, _ string) error {
	if profileID == "" {
		return nil
	}
	resumed, err := guard.resumeGuardedJobs(ctx, profileID)
	if err != nil {
		return err
	}
	retried, err := guard.retryFailedTasks(ctx, profileID)
	if err != nil {
		return err
	}
	slog.Default().Info("auth recovery finished",
		"profile", profileID, "jobs_resumed", resumed, "tasks_retried", retried)
	return nil
}

func (guard *AuthGuard) resumeGuardedJobs(ctx context.Context, profileID core.ProfileID) (int, error) {
	pauses, err := guard.pauses.JobPauses(ctx)
	if err != nil {
		return 0, err
	}
	resumed := 0
	for _, pause := range pauses {
		if pause.ProfileID != profileID || pause.Reason != AuthPauseReason {
			continue
		}
		count, err := guard.pauses.ResumeJob(ctx, pause.JobTag, profileID)
		if err != nil {
			return resumed, err
		}
		resumed += count
	}
	return resumed, nil
}

func (guard *AuthGuard) retryFailedTasks(ctx context.Context, profileID core.ProfileID) (int, error) {
	failed, err := guard.tasks.FailedAuthTasks(ctx, profileID, authRecoveryTaskLimit)
	if err != nil {
		return 0, err
	}
	retried := 0
	for _, item := range failed {
		if _, err := guard.control.Retry(ctx, item.ID); err != nil {
			slog.Default().Warn("auth recovery retry failed", "task", item.ID, "error", err)
			continue
		}
		retried++
	}
	return retried, nil
}

// jobTagFromSource extracts the job tag the scheduler or the manual run API
// recorded as the task source. Other sources (workflow children, dashboard
// actions) have no job to pause.
func jobTagFromSource(source string) string {
	for _, prefix := range []string{"cron:", "job-api:"} {
		if strings.HasPrefix(source, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(source, prefix))
		}
	}
	return ""
}
