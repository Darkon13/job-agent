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
	waiting, err := repository.ListReviewSessions(ctx, storage.ReviewSessionFilter{Status: core.ReviewWaiting})
	if err != nil || len(waiting) != 0 {
		t.Fatalf("waiting sessions: %#v err=%v", waiting, err)
	}
	answered, err := repository.ListReviewSessions(ctx, storage.ReviewSessionFilter{
		Status: core.ReviewAnswered, ProfileID: "profile-1", Platform: "hh",
	})
	if err != nil || len(answered) != 1 || answered[0].ID != session.ID {
		t.Fatalf("answered sessions: %#v err=%v", answered, err)
	}
	if other, err := repository.ListReviewSessions(ctx, storage.ReviewSessionFilter{ProfileID: "another"}); err != nil || len(other) != 0 {
		t.Fatalf("unrelated sessions: %#v err=%v", other, err)
	}
	if limited, err := repository.ListReviewSessions(ctx, storage.ReviewSessionFilter{Limit: 1}); err != nil || len(limited) != 1 {
		t.Fatalf("limited sessions: %#v err=%v", limited, err)
	}
}

func TestRepositoryFiltersAndCancelsReviewSessions(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	repository := NewRepository()
	newSession := func(tag, questionText string, at time.Time) core.ReviewSession {
		t.Helper()
		definition, err := core.NewProgressiveTestDefinition("hh", "vacancy-"+tag, "Test "+tag, nil, at)
		if err != nil {
			t.Fatalf("new definition: %v", err)
		}
		question := core.Question{ID: "question-" + tag, Text: questionText, Kind: core.QuestionText}
		if _, _, err := definition.ObserveQuestion(question, at); err != nil {
			t.Fatalf("observe question: %v", err)
		}
		if _, err := repository.UpsertTestDefinition(ctx, definition); err != nil {
			t.Fatalf("store definition: %v", err)
		}
		session, err := core.NewReviewSession(core.ReviewSessionID("review-"+tag), definition, "profile-1", core.CorrelationID("correlation-"+tag), at)
		if err != nil {
			t.Fatalf("new session: %v", err)
		}
		session.Questionnaire = core.Questionnaire{Title: definition.Title, Questions: []core.Question{question}}
		if _, err := repository.CreateReviewSession(ctx, session); err != nil {
			t.Fatalf("create session: %v", err)
		}
		return session
	}
	first := newSession("one", "Был ли у Вас опыт коммерческой разработки", now)
	second := newSession("two", "Какой у Вас ожидаемый уровень зарплаты", now.Add(time.Minute))

	found, err := repository.ListReviewSessions(ctx, storage.ReviewSessionFilter{Query: "коммерческой"})
	if err != nil || len(found) != 1 || found[0].ID != first.ID {
		t.Fatalf("query result=%#v err=%v", found, err)
	}
	page, err := repository.ListReviewSessions(ctx, storage.ReviewSessionFilter{Limit: 1, Offset: 1})
	if err != nil || len(page) != 1 || page[0].ID != first.ID {
		t.Fatalf("offset page=%#v err=%v", page, err)
	}

	cancelled := first
	cancelled.Status = core.ReviewCancelled
	cancelled.Revision = first.Revision + 1
	cancelled.UpdatedAt = now.Add(2 * time.Minute)
	if err := repository.CancelReviewSession(ctx, cancelled, first.Revision); err != nil {
		t.Fatalf("cancel session: %v", err)
	}
	if err := repository.CancelReviewSession(ctx, cancelled, first.Revision); !errors.Is(err, storage.ErrRevisionConflict) {
		t.Fatalf("repeat cancel error = %v", err)
	}
	visible, err := repository.ListReviewSessions(ctx, storage.ReviewSessionFilter{})
	if err != nil || len(visible) != 1 || visible[0].ID != second.ID {
		t.Fatalf("visible sessions=%#v err=%v", visible, err)
	}
	hidden, err := repository.ListReviewSessions(ctx, storage.ReviewSessionFilter{Status: core.ReviewCancelled})
	if err != nil || len(hidden) != 1 || hidden[0].ID != first.ID {
		t.Fatalf("cancelled sessions=%#v err=%v", hidden, err)
	}
}
