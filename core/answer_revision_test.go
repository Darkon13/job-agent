package core

import (
	"testing"
	"time"
)

func TestAnswerBlockRevisionValidationAndMerge(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	base := AnswerBlock{
		Tag: "hh-vacancy", Name: "HH vacancy", Kind: AnswerBlockVacancy, Platform: "hh",
		Answers: []StoredAnswer{
			{Question: "Explain experience", Text: "Five years of Go"},
			{Question: "Rate yourself", SelectedOptions: []string{"Junior"}},
		},
	}
	revision := AnswerBlockRevision{
		BlockTag: "hh-vacancy", Name: "HH vacancy", Kind: AnswerBlockVacancy, Platform: "hh",
		Revision: 1, Source: "review:review-1", Answers: base.Answers,
	}
	if err := revision.Validate(); err == nil {
		t.Fatal("expected missing created_at to fail")
	}
	revision.CreatedAt = now
	if err := revision.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	latest := AnswerBlock{
		Tag: "hh-vacancy", Name: "HH vacancy", Kind: AnswerBlockVacancy, Platform: "hh",
		Answers: []StoredAnswer{
			{Question: "Rate yourself", SelectedOptions: []string{"Senior"}},
			{Question: "Describe a project", Text: "Job agent"},
		},
	}
	merged, err := MergeAnswerBlocks(base, latest)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(merged.Answers) != 3 {
		t.Fatalf("merged = %#v", merged.Answers)
	}
	if merged.Answers[1].SelectedOptions[0] != "Senior" {
		t.Fatalf("override failed: %#v", merged.Answers[1])
	}
	if _, err := MergeAnswerBlocks(base, AnswerBlock{
		Tag: "other", Name: "Other", Kind: AnswerBlockVacancy, Platform: "telegram", Answers: latest.Answers,
	}); err == nil {
		t.Fatal("expected platform mismatch to fail")
	}

	first, err := StoredAnswersDigest(base.Answers)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	reordered, err := StoredAnswersDigest([]StoredAnswer{base.Answers[1], base.Answers[0]})
	if err != nil {
		t.Fatalf("digest reordered: %v", err)
	}
	if first != reordered {
		t.Fatal("digest must be order independent")
	}
}
