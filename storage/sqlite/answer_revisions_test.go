package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
)

func TestStoreAppendsAnswerBlockRevisions(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "job-agent.db")
	store, err := openStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	first, err := store.AppendAnswerBlockRevision(ctx, core.AnswerBlockRevision{
		BlockTag: "hh-vacancy", Name: "HH vacancy", Kind: core.AnswerBlockVacancy, Platform: "hh",
		Source: "review:review-1", CreatedAt: now,
		Answers: []core.StoredAnswer{{Question: "Explain experience", Text: "Five years of Go"}},
	})
	if err != nil {
		t.Fatalf("append first: %v", err)
	}
	if first.Revision != 1 {
		t.Fatalf("first revision = %#v", first)
	}
	second, err := store.AppendAnswerBlockRevision(ctx, core.AnswerBlockRevision{
		BlockTag: "hh-vacancy", Name: "HH vacancy", Kind: core.AnswerBlockVacancy, Platform: "hh",
		Source: "review:review-1", CreatedAt: now.Add(time.Minute),
		Answers: []core.StoredAnswer{
			{Question: "Explain experience", Text: "Five years of Go"},
			{Question: "Rate yourself", SelectedOptions: []string{"Senior"}},
		},
	})
	if err != nil {
		t.Fatalf("append second: %v", err)
	}
	if second.Revision != 2 {
		t.Fatalf("second revision = %#v", second)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	store, err = openStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	latest, found, err := store.LatestAnswerBlockRevision(ctx, "hh-vacancy")
	if err != nil || !found {
		t.Fatalf("latest: found=%v err=%v", found, err)
	}
	if latest.Revision != 2 || len(latest.Answers) != 2 || latest.Answers[1].SelectedOptions[0] != "Senior" {
		t.Fatalf("latest = %#v", latest)
	}
	if _, found, err := store.LatestAnswerBlockRevision(ctx, "missing"); err != nil || found {
		t.Fatalf("missing: found=%v err=%v", found, err)
	}
}
