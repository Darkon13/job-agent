package core

import (
	"reflect"
	"testing"
	"time"
)

func TestBuildTestDefinitionIsStableAndRetainsAllOptions(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	first := Questionnaire{Title: "Go", Questions: []Question{
		{ID: "q2", Text: "Explain", Kind: QuestionText},
		{ID: "q1", Text: "Choose", Kind: QuestionSingle, Options: []QuestionOption{
			{ID: "b", Text: "Beta"}, {ID: "a", Text: "Alpha"},
		}},
	}}
	second := Questionnaire{Title: "Go", Questions: []Question{
		{ID: "new-q1", Text: "choose", Kind: QuestionSingle, Options: []QuestionOption{
			{ID: "new-a", Text: "alpha"}, {ID: "new-b", Text: "BETA"},
		}},
		{ID: "new-q2", Text: "EXPLAIN", Kind: QuestionText},
	}}

	want, err := BuildTestDefinition("hh", first, now)
	if err != nil {
		t.Fatalf("build first: %v", err)
	}
	got, err := BuildTestDefinition("hh", second, now)
	if err != nil {
		t.Fatalf("build second: %v", err)
	}
	if got.ID != want.ID || got.LastAttemptFingerprint != want.LastAttemptFingerprint {
		t.Fatalf("definition identity changed: got %q, want %q", got.ID, want.ID)
	}
	if len(want.Questions) != 2 {
		t.Fatalf("all questions/options were not retained: %#v", want.Questions)
	}
	var choice TestQuestion
	for _, question := range want.Questions {
		if question.Kind == QuestionSingle {
			choice = question
		}
	}
	if !reflect.DeepEqual(choice.Options, []TestOption{{Text: "Alpha"}, {Text: "Beta"}}) {
		t.Fatalf("unexpected canonical options: %#v", choice.Options)
	}
}

func TestProgressiveDefinitionDoesNotRequireFutureQuestions(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	definition, err := NewProgressiveTestDefinition("hh", "go-medium", "Go", nil, now)
	if err != nil {
		t.Fatalf("new definition: %v", err)
	}
	if len(definition.Questions) != 0 || definition.LastAttemptFingerprint != "" {
		t.Fatalf("new progressive definition already has content: %#v", definition)
	}
	question := Question{ID: "runtime-1", Text: "Choose", Kind: QuestionSingle, Options: []QuestionOption{{ID: "a", Text: "A"}, {ID: "b", Text: "B"}}}
	fingerprint, created, err := definition.ObserveQuestion(question, now.Add(time.Minute))
	if err != nil || !created {
		t.Fatalf("observe question: created=%v err=%v", created, err)
	}
	if len(definition.Questions) != 1 || definition.Questions[0].Fingerprint != fingerprint {
		t.Fatalf("question was not catalogued: %#v", definition.Questions)
	}
}

func TestCodeQuestionCanBeCataloguedButCannotBeAutoResolved(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	questionnaire := Questionnaire{Questions: []Question{
		{ID: "code-1", Text: "Implement a function", Kind: QuestionCode},
	}}
	definition, err := BuildTestDefinition("hh", questionnaire, now)
	if err != nil {
		t.Fatalf("build code definition: %v", err)
	}
	if definition.Questions[0].Kind != QuestionCode {
		t.Fatalf("unexpected kind: %q", definition.Questions[0].Kind)
	}

	block := AnswerBlock{
		Tag: "code", Name: "Code", Kind: AnswerBlockQualification, Platform: "hh",
		Answers: []StoredAnswer{{Question: "Implement a function", Text: "package main"}},
	}
	if _, err := ResolveAnswerBlock(questionnaire, block); err == nil {
		t.Fatal("expected code answer resolution to be rejected")
	}

	session, err := NewReviewSession("review-1", definition, "profile-1", "correlation-1", now)
	if err != nil {
		t.Fatalf("new review session: %v", err)
	}
	prompt := ReviewPrompt{
		ID: "prompt-code", SessionID: session.ID, Revision: session.Revision,
		Question: questionnaire.Questions[0], CreatedAt: now,
	}
	if err := session.WaitForAnswer(prompt, now.Add(time.Minute)); err == nil {
		t.Fatal("expected code review prompt to be rejected")
	}
	if session.Status != ReviewUnsupported {
		t.Fatalf("got status %q, want %q after code prompt", session.Status, ReviewUnsupported)
	}
}

func TestReviewSelectionKeepsOptionsAndRejectsStaleClient(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	definition, err := BuildTestDefinition("hh", Questionnaire{Questions: []Question{
		{ID: "q1", Text: "Choose", Kind: QuestionSingle, Options: []QuestionOption{
			{ID: "a", Text: "Alpha"}, {ID: "b", Text: "Beta"},
		}},
	}}, now)
	if err != nil {
		t.Fatalf("build definition: %v", err)
	}
	session, err := NewReviewSession("review-1", definition, "profile-1", "correlation-1", now)
	if err != nil {
		t.Fatalf("new review session: %v", err)
	}
	prompt := ReviewPrompt{
		ID: "prompt-1", SessionID: session.ID, Revision: session.Revision,
		Question: Question{ID: "runtime-q1", Text: "Choose", Kind: QuestionSingle, Options: []QuestionOption{
			{ID: "runtime-a", Text: "Alpha"}, {ID: "runtime-b", Text: "Beta"},
		}}, CreatedAt: now,
	}
	if err := session.WaitForAnswer(prompt, now); err != nil {
		t.Fatalf("wait for answer: %v", err)
	}
	selection, err := session.RecordSelection(prompt, []string{"Beta"}, "", "telegram", 1, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("record selection: %v", err)
	}
	if selection.Assessment != AssessmentUnverified || selection.SelectedOptions[0] != "Beta" {
		t.Fatalf("unexpected selection: %#v", selection)
	}
	if len(definition.Questions[0].Options) != 2 {
		t.Fatal("catalog options were lost after selection")
	}
	if _, err := session.RecordSelection(prompt, []string{"Alpha"}, "", "rest", 1, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expected stale selection to be rejected")
	}
}

func TestReviewSessionCompletesOnlyAfterRecordedAnswer(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	definition, err := NewProgressiveTestDefinition("hh", "docker-basic", "Docker", nil, now)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	session, err := NewReviewSession("review-1", definition, "profile-1", "correlation-1", now)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if err := session.Complete(now.Add(time.Second)); err == nil {
		t.Fatal("pending session completed without an answer")
	}
	session.Status = ReviewAnswered
	if err := session.Complete(now.Add(time.Second)); err != nil {
		t.Fatalf("complete answered session: %v", err)
	}
	if session.Status != ReviewCompleted {
		t.Fatalf("unexpected status %s", session.Status)
	}
}
