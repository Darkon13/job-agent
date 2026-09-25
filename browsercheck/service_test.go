package browsercheck

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

type testDriver struct {
	submitOutcome Outcome
	answerOutcome Outcome
	submitCalls   int
	answerCalls   int
	lastVacancy   string
	lastLetter    string
	lastAnswer    string
	block         chan struct{}
}

func (driver *testDriver) Submit(_ context.Context, _ core.ProfileID, vacancyID string, letter string) (Outcome, error) {
	driver.submitCalls++
	driver.lastVacancy = vacancyID
	driver.lastLetter = letter
	if driver.block != nil {
		<-driver.block
	}
	return driver.submitOutcome, nil
}

func (driver *testDriver) Answer(_ context.Context, _ core.ProfileID, answer string, letter string) (Outcome, error) {
	driver.answerCalls++
	driver.lastAnswer = answer
	driver.lastLetter = letter
	return driver.answerOutcome, nil
}

type testApplications struct {
	application core.Application
	err         error
}

func (fake testApplications) ApplicationByID(context.Context, core.ApplicationID) (core.Application, error) {
	return fake.application, fake.err
}

type testRetry struct {
	calls int
	task  core.Task
}

func (fake *testRetry) Enqueue(context.Context, core.ApplicationID, string) (core.Task, bool, error) {
	fake.calls++
	return fake.task, true, nil
}

type testClock struct{ now time.Time }

func (clock *testClock) Now() time.Time { return clock.now }

type testIDs struct{ next int }

func (ids *testIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}

func newCheckService(t *testing.T, driver Driver, applications ApplicationReader, retry RetryEnqueuer, clock *testClock) *Service {
	t.Helper()
	service, err := NewService(driver, applications, retry, clock, &testIDs{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

func checkApplication(t *testing.T, prepared string) core.Application {
	t.Helper()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	application, err := core.NewApplication(
		"application-1",
		core.ApplicationKey{ProfileID: "primary", Vacancy: core.VacancyKey{Platform: "hh", ExternalID: "137786726"}},
		now,
	)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	application.PreparedMessage = prepared
	return application
}

func TestCheckStartStoresCaptchaAndAnswerConfirms(t *testing.T) {
	driver := &testDriver{
		submitOutcome: Outcome{State: StateWaitingCaptcha, Image: []byte("png"), Message: "введите символы"},
		answerOutcome: Outcome{State: StateDone, Message: "отклик отправлен из браузера"},
	}
	retry := &testRetry{task: core.Task{ID: "task-1"}}
	clock := &testClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	service := newCheckService(t, driver, testApplications{application: checkApplication(t, "письмо")}, retry, clock)

	session, err := service.Start(context.Background(), "application-1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if session.State != StateWaitingCaptcha || !session.HasImage || session.ID == "" {
		t.Fatalf("session=%#v", session)
	}
	if driver.submitCalls != 1 || driver.lastVacancy != "137786726" || driver.lastLetter != "письмо" {
		t.Fatalf("driver=%#v", driver)
	}
	if image, err := service.Image(context.Background(), "application-1", session.ID); err != nil || string(image) != "png" {
		t.Fatalf("image=%q err=%v", image, err)
	}

	repeated, err := service.Start(context.Background(), "application-1")
	if err != nil || repeated.ID != session.ID || driver.submitCalls != 1 {
		t.Fatalf("repeated start=%#v err=%v calls=%d", repeated, err, driver.submitCalls)
	}

	answered, err := service.Answer(context.Background(), "application-1", session.ID, " 42 ")
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if answered.State != StateDone || driver.lastAnswer != "42" || retry.calls != 1 {
		t.Fatalf("answered=%#v answer=%q retry=%d", answered, driver.lastAnswer, retry.calls)
	}
	if answered.Message == "" {
		t.Fatalf("done session has no confirmation message: %#v", answered)
	}
}

func TestCheckDoneSkipsRetryWhenAlreadySubmitted(t *testing.T) {
	driver := &testDriver{submitOutcome: Outcome{State: StateDone, Message: "отклик уже существует на странице вакансии"}}
	retry := &testRetry{}
	clock := &testClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	application := checkApplication(t, "")
	application.Status = core.ApplicationSubmitted
	service := newCheckService(t, driver, testApplications{application: application}, retry, clock)

	session, err := service.Start(context.Background(), "application-1")
	if err != nil || session.State != StateDone || retry.calls != 0 {
		t.Fatalf("session=%#v err=%v retry=%d", session, err, retry.calls)
	}
}

func TestCheckAnswerRejectsNonCaptchaSession(t *testing.T) {
	driver := &testDriver{submitOutcome: Outcome{State: StateDone}}
	clock := &testClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	service := newCheckService(t, driver, testApplications{application: checkApplication(t, "")}, &testRetry{}, clock)

	session, err := service.Start(context.Background(), "application-1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := service.Answer(context.Background(), "application-1", session.ID, "42"); !errors.Is(err, ErrSessionState) {
		t.Fatalf("answer error=%v", err)
	}
}

func TestCheckSessionExpiresAndCancels(t *testing.T) {
	driver := &testDriver{submitOutcome: Outcome{State: StateWaitingCaptcha, Image: []byte("png")}}
	clock := &testClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	service := newCheckService(t, driver, testApplications{application: checkApplication(t, "")}, &testRetry{}, clock)

	session, err := service.Start(context.Background(), "application-1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := service.Cancel("application-1", session.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := service.Status("application-1", session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("status after cancel=%v", err)
	}

	expiring, err := service.Start(context.Background(), "application-1")
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	clock.now = clock.now.Add(DefaultSessionTTL + time.Minute)
	if _, err := service.Status("application-1", expiring.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("status after expiry=%v", err)
	}
	if _, err := service.Start(context.Background(), "application-1"); err != nil || driver.submitCalls != 3 {
		t.Fatalf("start after expiry err=%v calls=%d", err, driver.submitCalls)
	}
}

func TestCheckStartRejectsConcurrentApplication(t *testing.T) {
	block := make(chan struct{})
	driver := &testDriver{submitOutcome: Outcome{State: StateDone}, block: block}
	clock := &testClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	service := newCheckService(t, driver, testApplications{application: checkApplication(t, "")}, &testRetry{}, clock)

	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = service.Start(context.Background(), "application-1")
	}()
	<-started
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		service.mu.Lock()
		busy := len(service.busy)
		service.mu.Unlock()
		if busy == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := service.Start(context.Background(), "application-1"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("concurrent start=%v", err)
	}
	close(block)
}
