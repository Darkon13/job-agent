package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func TestRepositoryKeepsProgressiveReviewStateIsolated(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	repository := NewRepository()
	definition, err := core.NewProgressiveTestDefinition("hh", "git-basic", "Git", nil, now)
	if err != nil {
		t.Fatalf("new definition: %v", err)
	}
	question := core.Question{ID: "runtime-1", Text: "Выберите команду", Kind: core.QuestionSingle, Options: []core.QuestionOption{
		{ID: "a", Text: "git status"}, {ID: "b", Text: "git push"},
	}}
	if _, _, err := definition.ObserveQuestion(question, now.Add(time.Minute)); err != nil {
		t.Fatalf("observe question: %v", err)
	}
	if created, err := repository.UpsertTestDefinition(ctx, definition); err != nil || !created {
		t.Fatalf("store definition: created=%v err=%v", created, err)
	}
	loaded, err := repository.TestDefinition(ctx, definition.ID)
	if err != nil {
		t.Fatalf("load definition: %v", err)
	}
	loaded.Questions[0].Options[0].Text = "mutated"
	again, err := repository.TestDefinition(ctx, definition.ID)
	if err != nil || again.Questions[0].Options[0].Text == "mutated" {
		t.Fatalf("repository exposed mutable catalog state: %#v err=%v", again, err)
	}

	session, err := core.NewReviewSession("review-1", definition, "profile-1", "correlation-1", now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("new review session: %v", err)
	}
	if _, err := repository.CreateReviewSession(ctx, session); err != nil {
		t.Fatalf("create review session: %v", err)
	}
	prompt := core.ReviewPrompt{
		ID: "prompt-1", SessionID: session.ID, Revision: session.Revision,
		Question: question, CreatedAt: now.Add(3 * time.Minute),
	}
	if err := session.WaitForAnswer(prompt, prompt.CreatedAt); err != nil {
		t.Fatalf("wait for answer: %v", err)
	}
	if err := repository.SaveReviewPrompt(ctx, session, prompt, 1); err != nil {
		t.Fatalf("save prompt: %v", err)
	}
	selection, err := session.RecordSelection(prompt, []string{"git status"}, "", "rest", 1, now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("record selection: %v", err)
	}
	if err := repository.AppendReviewSelection(ctx, session, selection, 1); err != nil {
		t.Fatalf("append selection: %v", err)
	}
	if err := repository.AppendReviewSelection(ctx, session, selection, 1); !errors.Is(err, storage.ErrRevisionConflict) {
		t.Fatalf("duplicate selection error = %v, want revision conflict", err)
	}
	selections, err := repository.ReviewSelections(ctx, session.ID)
	if err != nil || len(selections) != 1 {
		t.Fatalf("unexpected selections: %#v err=%v", selections, err)
	}
}
