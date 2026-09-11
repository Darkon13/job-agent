package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

type fakeQualificationAttemptService struct {
	questions   []core.Question
	submitted   []core.ResolvedAnswer
	result      core.QualificationResult
	startCalls  int
	finishCalls int
	startErr    error
	questionErr error
}

func (fake *fakeQualificationAttemptService) StartQualification(_ context.Context, profileID core.ProfileID, offeringID core.QualificationID) (adapter.QualificationSession, error) {
	fake.startCalls++
	if fake.startErr != nil {
		return adapter.QualificationSession{}, fake.startErr
	}
	return adapter.QualificationSession{ProfileID: profileID, OfferingID: offeringID, AttemptID: "attempt-1"}, nil
}

func (fake *fakeQualificationAttemptService) CurrentQuestion(context.Context, adapter.QualificationSession) (core.Question, error) {
	if fake.questionErr != nil {
		return core.Question{}, fake.questionErr
	}
	if len(fake.submitted) >= len(fake.questions) {
		return core.Question{}, errors.New("attempt has no current question")
	}
	return fake.questions[len(fake.submitted)], nil
}

func (fake *fakeQualificationAttemptService) SubmitAnswer(_ context.Context, _ adapter.QualificationSession, answer core.ResolvedAnswer) error {
	fake.submitted = append(fake.submitted, answer)
	return nil
}

func (fake *fakeQualificationAttemptService) AttemptResult(context.Context, adapter.QualificationSession) (core.QualificationResult, bool, error) {
	if len(fake.submitted) < len(fake.questions) {
		return core.QualificationResult{}, false, nil
	}
	return fake.result, true, nil
}

func (fake *fakeQualificationAttemptService) FinishQualification(context.Context, adapter.QualificationSession) error {
	fake.finishCalls++
	return nil
}

func qualificationRunnerFixture(t *testing.T, blockQuestions int) (*QualificationStartHandler, *fakeQualificationAttemptService, *storagememory.Repository, core.QualificationDescriptor) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	order := 1
	descriptor := core.QualificationDescriptor{
		FamilyID: "go", FamilyName: "Go", LevelID: "medium", LevelName: "Средний", LevelOrder: &order,
	}
	repository := storagememory.NewRepository()
	offering := core.QualificationOffering{
		ID: "hh:offer-1", Platform: "hh", ProfileID: "primary", ExternalID: "go-medium",
		Qualification: descriptor, Status: core.QualificationAvailable, ObservedAt: now,
	}
	if err := repository.UpsertQualificationOfferings(ctx, "hh", "primary", []core.QualificationOffering{offering}); err != nil {
		t.Fatalf("seed offering: %v", err)
	}
	score, maxScore := 9.0, 10.0
	base, err := core.NewProgressiveTestDefinition("hh", "go-medium", "Go", &descriptor, now)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if _, err := repository.UpsertTestDefinition(ctx, base); err != nil {
		t.Fatalf("seed definition: %v", err)
	}
	questions := []core.Question{
		{ID: "runtime-1", Text: "Explain experience", Kind: core.QuestionText},
		{ID: "runtime-2", Text: "Describe a project", Kind: core.QuestionText},
	}
	answers := make([]core.StoredAnswer, 0, blockQuestions)
	for _, question := range questions[:blockQuestions] {
		fingerprint, err := core.QuestionFingerprint(question)
		if err != nil {
			t.Fatalf("fingerprint: %v", err)
		}
		answers = append(answers, core.StoredAnswer{Question: question.Text, QuestionFingerprint: fingerprint, Text: "Answer for " + question.ID})
	}
	block := core.AnswerBlock{
		Tag: "hh-go-medium", Name: "Go medium", Kind: core.AnswerBlockQualification,
		Platform: "hh", Qualification: &descriptor, Answers: answers,
	}
	blocks, err := core.NewAnswerBlockRegistry(block)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	service := &fakeQualificationAttemptService{questions: questions, result: core.QualificationResult{
		Status: core.QualificationPassed, Score: &score, MaxScore: &maxScore,
		Verified: true, AnswerBlockTag: "hh-go-medium", CompletedAt: now,
	}}
	attempts := NewQualificationAttemptRegistry()
	if err := attempts.Register("primary", service); err != nil {
		t.Fatalf("register: %v", err)
	}
	handler, err := NewQualificationStartHandler(attempts, repository, repository, repository, blocks, fixedClock{now: now})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return handler, service, repository, descriptor
}

func qualificationStartTask(t *testing.T) core.Task {
	t.Helper()
	payload, err := json.Marshal(core.SkillVerificationStartPayload{
		ProfileID: "primary", Platform: "hh", OfferingID: "hh:offer-1",
	})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return core.Task{
		ID: "start-task", Type: core.TaskSkillVerificationStart, ProfileID: "primary",
		CorrelationID: "correlation-1", IdempotencyKey: "start-1", Payload: payload,
	}
}

func TestQualificationStartAnswersFromReusableBlock(t *testing.T) {
	handler, service, repository, descriptor := qualificationRunnerFixture(t, 2)
	if err := handler.Handle(context.Background(), qualificationStartTask(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if service.startCalls != 1 || len(service.submitted) != 2 || service.finishCalls != 0 {
		t.Fatalf("service = %#v", service)
	}
	if service.submitted[1].Text != "Answer for runtime-2" {
		t.Fatalf("submitted = %#v", service.submitted)
	}
	best, found, err := repository.BestQualificationResult(context.Background(), "hh", "primary", "go", "medium")
	if err != nil || !found || !best.Passed() {
		t.Fatalf("best = %#v found=%v err=%v", best, found, err)
	}
	_ = descriptor
}

func TestQualificationStartFinishesEarlyWithoutAnswer(t *testing.T) {
	handler, service, _, _ := qualificationRunnerFixture(t, 1)
	err := handler.Handle(context.Background(), qualificationStartTask(t))
	requireQualificationCategory(t, err, core.ErrorValidationRequired)
	if service.finishCalls != 1 || len(service.submitted) != 1 {
		t.Fatalf("service = %#v", service)
	}
}

func TestQualificationStartSkipsPassedLevel(t *testing.T) {
	handler, service, repository, descriptor := qualificationRunnerFixture(t, 1)
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	score, maxScore := 9.0, 10.0
	if _, _, err := repository.SaveQualificationAttempt(context.Background(), core.QualificationAttempt{
		Platform: "hh", ProfileID: "primary", Qualification: descriptor,
		Result: core.QualificationResult{
			Status: core.QualificationPassed, Score: &score, MaxScore: &maxScore,
			Verified: true, AnswerBlockTag: "hh-go-medium", CompletedAt: now,
		},
	}, now); err != nil {
		t.Fatalf("seed best: %v", err)
	}
	if err := handler.Handle(context.Background(), qualificationStartTask(t)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if service.startCalls != 0 {
		t.Fatalf("startCalls = %d", service.startCalls)
	}
}

func requireQualificationCategory(t *testing.T, err error, category core.ErrorCategory) {
	t.Helper()
	var operationErr *core.OperationError
	if !errors.As(err, &operationErr) || operationErr.Category != category {
		t.Fatalf("error = %v, want category %s", err, category)
	}
}
