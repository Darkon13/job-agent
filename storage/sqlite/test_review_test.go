package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

func TestStorePersistsProgressiveTestCatalogAcrossReopen(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	levelOrder := 1
	qualification := core.QualificationDescriptor{
		FamilyID: "go", FamilyName: "Go", LevelID: "medium", LevelName: "Средний", LevelOrder: &levelOrder,
	}
	definition, err := core.NewProgressiveTestDefinition("hh", "go-medium", "Go: средний", &qualification, now)
	if err != nil {
		t.Fatalf("new test definition: %v", err)
	}
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	created, err := store.UpsertTestDefinition(ctx, definition)
	if err != nil || !created {
		t.Fatalf("insert empty definition: created=%v err=%v", created, err)
	}

	first := core.Question{ID: "runtime-1", Text: "Как объявить переменную?", Kind: core.QuestionSingle, Options: []core.QuestionOption{
		{ID: "a", Text: "var x = 1"}, {ID: "b", Text: "x := 1"},
	}}
	firstFingerprint, added, err := definition.ObserveQuestion(first, now.Add(time.Minute))
	if err != nil || !added {
		t.Fatalf("observe first question: added=%v err=%v", added, err)
	}
	if created, err := store.UpsertTestDefinition(ctx, definition); err != nil || created {
		t.Fatalf("update first question: created=%v err=%v", created, err)
	}

	changed := first
	changed.ID = "runtime-2"
	changed.Options = append(changed.Options, core.QuestionOption{ID: "c", Text: "let x = 1"})
	secondFingerprint, added, err := definition.ObserveQuestion(changed, now.Add(2*time.Minute))
	if err != nil || !added || secondFingerprint == firstFingerprint {
		t.Fatalf("observe changed question: fingerprint=%s added=%v err=%v", secondFingerprint, added, err)
	}
	if _, err := definition.CompleteObservedAttempt([]string{secondFingerprint}, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("complete observed attempt: %v", err)
	}
	if _, err := store.UpsertTestDefinition(ctx, definition); err != nil {
		t.Fatalf("persist completed attempt: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := openStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	stored, err := reopened.TestDefinition(ctx, definition.ID)
	if err != nil {
		t.Fatalf("load definition: %v", err)
	}
	if len(stored.Questions) != 2 || stored.ObservedAttempts != 1 || stored.LastAttemptFingerprint == "" {
		t.Fatalf("progressive catalog was not persisted: %#v", stored)
	}
	listed, err := reopened.ListTestDefinitions(ctx, storage.TestDefinitionFilter{
		Platform: "hh", FamilyID: "go", LevelID: "medium",
	})
	if err != nil {
		t.Fatalf("list definitions: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != definition.ID {
		t.Fatalf("unexpected catalog listing: %#v", listed)
	}
}

func TestStoreAppendsOneReviewSelectionForConcurrentClients(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	store, err := openStore(filepath.Join(t.TempDir(), "job-agent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	definition, err := core.NewProgressiveTestDefinition("hh", "go-medium", "Go", nil, now)
	if err != nil {
		t.Fatalf("new definition: %v", err)
	}
	if _, err := store.UpsertTestDefinition(ctx, definition); err != nil {
		t.Fatalf("store definition: %v", err)
	}
	session, err := core.NewReviewSession("review-1", definition, "profile-1", "correlation-1", now)
	if err != nil {
		t.Fatalf("new review session: %v", err)
	}
	if created, err := store.CreateReviewSession(ctx, session); err != nil || !created {
		t.Fatalf("create review session: created=%v err=%v", created, err)
	}
	prompt := core.ReviewPrompt{
		ID: "prompt-1", SessionID: session.ID, Revision: session.Revision,
		Question: core.Question{ID: "runtime-1", Text: "Выберите вариант", Kind: core.QuestionSingle, Options: []core.QuestionOption{
			{ID: "a", Text: "Ответ А"}, {ID: "b", Text: "Ответ Б"},
		}}, CreatedAt: now.Add(time.Minute),
	}
	if err := session.WaitForAnswer(prompt, prompt.CreatedAt); err != nil {
		t.Fatalf("wait for answer: %v", err)
	}
	if err := store.SaveReviewPrompt(ctx, session, prompt, 1); err != nil {
		t.Fatalf("save review prompt: %v", err)
	}
	storedPrompt, err := store.ReviewPrompt(ctx, prompt.ID)
	if err != nil || len(storedPrompt.Question.Options) != 2 {
		t.Fatalf("review prompt lost options: %#v err=%v", storedPrompt, err)
	}

	type outcome struct{ err error }
	outcomes := make(chan outcome, 2)
	var start sync.WaitGroup
	start.Add(1)
	for index, option := range []string{"Ответ А", "Ответ Б"} {
		index, option := index, option
		go func() {
			candidate := session
			selection, err := candidate.RecordSelection(prompt, []string{option}, "", "client-"+string(rune('a'+index)), 1, now.Add(2*time.Minute))
			if err == nil {
				start.Wait()
				err = store.AppendReviewSelection(ctx, candidate, selection, 1)
			}
			outcomes <- outcome{err: err}
		}()
	}
	start.Done()

	var succeeded, conflicted int
	for range 2 {
		result := <-outcomes
		switch {
		case result.err == nil:
			succeeded++
		case errors.Is(result.err, storage.ErrRevisionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent answer error: %v", result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("unexpected concurrent outcomes: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	storedSession, err := store.ReviewSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("load review session: %v", err)
	}
	selections, err := store.ReviewSelections(ctx, session.ID)
	if err != nil {
		t.Fatalf("load review selections: %v", err)
	}
	if storedSession.Revision != 2 || storedSession.Status != core.ReviewAnswered || len(selections) != 1 {
		t.Fatalf("review state is not atomic: session=%#v selections=%#v", storedSession, selections)
	}
	if err := storedSession.Complete(now.Add(3 * time.Minute)); err != nil {
		t.Fatalf("complete review session: %v", err)
	}
	if err := store.FinishReviewSession(ctx, storedSession, 2); err != nil {
		t.Fatalf("finish review session: %v", err)
	}
	finished, err := store.ReviewSession(ctx, session.ID)
	if err != nil || finished.Status != core.ReviewCompleted {
		t.Fatalf("review session was not finished: %#v err=%v", finished, err)
	}
	if err := store.FinishReviewSession(ctx, storedSession, 2); !errors.Is(err, storage.ErrRevisionConflict) {
		t.Fatalf("second finish error = %v, want revision conflict", err)
	}
}
