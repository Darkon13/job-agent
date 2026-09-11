package core

import "testing"

func TestVacancyAnswerBlockResolution(t *testing.T) {
	questionnaire := Questionnaire{Questions: []Question{
		{ID: "1", Text: "Explain experience", Kind: QuestionText},
		{ID: "2", Text: "Pick a language", Kind: QuestionSingle, Options: []QuestionOption{
			{ID: "10", Text: "Go"}, {ID: "11", Text: "Python"},
		}},
	}}
	block := AnswerBlock{
		Tag: "hh-vacancy", Name: "HH vacancy", Kind: AnswerBlockVacancy, Platform: "hh",
		Answers: []StoredAnswer{
			{Question: "Explain experience", Text: "Five years of Go"},
			{Question: "  pick   a language ", SelectedOptions: []string{"Go"}},
		},
	}
	if err := ValidateAnswerBlock(block); err != nil {
		t.Fatalf("validate: %v", err)
	}
	plan, err := ResolveAnswerBlock(questionnaire, block)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(plan.Answers) != 2 || plan.Answers[1].SelectedOptionIDs[0] != "10" {
		t.Fatalf("plan = %#v", plan)
	}
	missing, err := UncoveredQuestions(questionnaire, block)
	if err != nil || len(missing) != 0 {
		t.Fatalf("coverage: missing=%#v err=%v", missing, err)
	}
	partial := block
	partial.Answers = partial.Answers[:1]
	missing, err = UncoveredQuestions(questionnaire, partial)
	if err != nil || len(missing) != 1 || missing[0].ID != "2" {
		t.Fatalf("partial coverage: missing=%#v err=%v", missing, err)
	}
	registry, err := NewAnswerBlockRegistry(block)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if stored, found := registry.FindVacancy("hh"); !found || stored.Tag != "hh-vacancy" {
		t.Fatalf("find vacancy: %#v found=%v", stored, found)
	}
	if _, found := registry.FindVacancy("telegram"); found {
		t.Fatal("unexpected vacancy block")
	}
	if _, err := NewAnswerBlockRegistry(block, AnswerBlock{
		Tag: "other", Name: "Other", Kind: AnswerBlockVacancy, Platform: "hh", Answers: block.Answers,
	}); err == nil {
		t.Fatal("expected duplicate vacancy platform error")
	}
	if err := ValidateAnswerBlock(AnswerBlock{
		Tag: "bad", Name: "Bad", Kind: AnswerBlockVacancy, Platform: "hh",
		Match: AnswerBlockMatcher{Topic: "topic"}, Answers: block.Answers,
	}); err == nil {
		t.Fatal("expected vacancy matcher validation error")
	}
}
