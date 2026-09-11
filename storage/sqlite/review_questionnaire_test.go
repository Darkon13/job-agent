package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestStorePersistsReviewSessionQuestionnaire(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	definition, err := core.NewProgressiveTestDefinition("hh", "vacancy:42", "Go basics", nil, now)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if _, err := store.UpsertTestDefinition(ctx, definition); err != nil {
		t.Fatalf("store definition: %v", err)
	}
	session, err := core.NewReviewSession("review-1", definition, "primary", "correlation-1", now)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	session.Questionnaire = core.Questionnaire{
		Title: "Go basics",
		Questions: []core.Question{
			{ID: "runtime-1", Text: "Rate yourself", Kind: core.QuestionSingle, Options: []core.QuestionOption{
				{ID: "21", Text: "Senior"},
			}},
		},
	}
	if _, err := store.CreateReviewSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	stored, err := store.ReviewSession(ctx, "review-1")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if len(stored.Questionnaire.Questions) != 1 || stored.Questionnaire.Questions[0].Options[0].ID != "21" {
		t.Fatalf("questionnaire = %#v", stored.Questionnaire)
	}
}
