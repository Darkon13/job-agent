package worker

import (
	"context"
	"testing"
	"time"

	"github.com/Darkon13/job-agent/core"
	storagememory "github.com/Darkon13/job-agent/storage/memory"
)

func TestReviewedVacancyAnswersMergeLatestRevision(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	repository := storagememory.NewRepository()
	block := core.AnswerBlock{
		Tag: "hh-vacancy", Name: "HH vacancy", Kind: core.AnswerBlockVacancy, Platform: "hh",
		Answers: []core.StoredAnswer{{Question: "Explain experience", Text: "Five years of Go"}},
	}
	registry, err := core.NewAnswerBlockRegistry(block)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if _, err := repository.AppendAnswerBlockRevision(ctx, core.AnswerBlockRevision{
		BlockTag: "hh-vacancy", Name: "HH vacancy", Kind: core.AnswerBlockVacancy, Platform: "hh",
		Source: "review:review-1", CreatedAt: now,
		Answers: []core.StoredAnswer{
			{Question: "Explain experience", Text: "Five years of Go"},
			{Question: "Rate yourself", SelectedOptions: []string{"Senior"}},
		},
	}); err != nil {
		t.Fatalf("append revision: %v", err)
	}
	resolver, err := NewReviewedVacancyAnswers(registry, repository)
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	merged, found, err := resolver.FindVacancy(ctx, "hh")
	if err != nil || !found {
		t.Fatalf("find: found=%v err=%v", found, err)
	}
	if len(merged.Answers) != 2 || merged.Answers[1].Question != "Rate yourself" {
		t.Fatalf("merged = %#v", merged.Answers)
	}
	if _, found, err := resolver.FindVacancy(ctx, "telegram"); err != nil || found {
		t.Fatalf("unknown platform: found=%v err=%v", found, err)
	}
}
