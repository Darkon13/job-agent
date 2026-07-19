package core

import "testing"

func TestObservedAttemptFingerprintIgnoresQuestionAndOptionOrder(t *testing.T) {
	first := Questionnaire{Questions: []Question{
		{ID: "q1", Text: "Choose a command", Kind: QuestionSingle, Options: []QuestionOption{{ID: "a", Text: "git status"}, {ID: "b", Text: "git push"}}},
		{ID: "q2", Text: "Explain the result", Kind: QuestionText},
	}}
	second := Questionnaire{Questions: []Question{
		{ID: "runtime-2", Text: "  explain   the RESULT ", Kind: QuestionText},
		{ID: "runtime-1", Text: "choose a command", Kind: QuestionSingle, Options: []QuestionOption{{ID: "new-b", Text: "GIT PUSH"}, {ID: "new-a", Text: "git status"}}},
	}}

	want, err := ObservedAttemptFingerprint(first)
	if err != nil {
		t.Fatalf("fingerprint first: %v", err)
	}
	got, err := ObservedAttemptFingerprint(second)
	if err != nil {
		t.Fatalf("fingerprint second: %v", err)
	}
	if got != want {
		t.Fatalf("fingerprints differ: got %s, want %s", got, want)
	}
}

func TestResolveQuestionAnswerUsesOnlyCurrentQuestionFingerprint(t *testing.T) {
	question := Question{ID: "runtime-current", Text: "Choose a command", Kind: QuestionSingle, Options: []QuestionOption{
		{ID: "runtime-push", Text: "git push"}, {ID: "runtime-status", Text: "git status"},
	}}
	fingerprint, err := QuestionFingerprint(question)
	if err != nil {
		t.Fatalf("question fingerprint: %v", err)
	}
	block := AnswerBlock{
		Tag: "git", Name: "Git", Kind: AnswerBlockQualification, Platform: "hh",
		Answers: []StoredAnswer{{
			Question: "Choose a command", QuestionFingerprint: fingerprint,
			SelectedOptions: []string{"git status"},
		}},
	}
	resolved, err := ResolveQuestionAnswer(question, block)
	if err != nil {
		t.Fatalf("resolve current question: %v", err)
	}
	if resolved.QuestionID != "runtime-current" || len(resolved.SelectedOptionIDs) != 1 || resolved.SelectedOptionIDs[0] != "runtime-status" {
		t.Fatalf("unexpected resolved answer: %#v", resolved)
	}

	changed := question
	changed.Options = append(changed.Options, QuestionOption{ID: "runtime-new", Text: "git restore"})
	if _, err := ResolveQuestionAnswer(changed, block); err == nil {
		t.Fatal("expected changed current question to require review")
	}
}

func TestResolveAnswerBlockUsesTextInsteadOfOrderOrRuntimeIDs(t *testing.T) {
	questionnaire := Questionnaire{Questions: []Question{
		{ID: "runtime-text", Text: "Explain the result", Kind: QuestionText},
		{ID: "runtime-choice", Text: "Choose a command", Kind: QuestionSingle, Options: []QuestionOption{{ID: "wrong-runtime", Text: "git push"}, {ID: "right-runtime", Text: "git status"}}},
	}}
	block := AnswerBlock{
		Tag: "reviewed-git-example", Name: "Git example", Kind: AnswerBlockQualification, Platform: "mock",
		Answers: []StoredAnswer{
			{Question: "choose a command", SelectedOptions: []string{"GIT STATUS"}},
			{Question: "Explain the result", Text: "A user-provided explanation."},
		},
	}

	plan, err := ResolveAnswerBlock(questionnaire, block)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if plan.Answers[0].QuestionID != "runtime-text" || plan.Answers[0].Text == "" {
		t.Fatalf("unexpected text answer: %#v", plan.Answers[0])
	}
	if got := plan.Answers[1].SelectedOptionIDs; len(got) != 1 || got[0] != "right-runtime" {
		t.Fatalf("unexpected selected options: %#v", got)
	}
}

func TestResolveAnswerBlockRejectsChangedQuestion(t *testing.T) {
	questionnaire := Questionnaire{Questions: []Question{
		{ID: "q1", Text: "New wording", Kind: QuestionSingle, Options: []QuestionOption{{ID: "a", Text: "One"}}},
	}}
	block := AnswerBlock{
		Tag: "old-set", Name: "Old set", Kind: AnswerBlockQualification, Platform: "mock",
		Answers: []StoredAnswer{{Question: "Old wording", SelectedOptions: []string{"One"}}},
	}

	if _, err := ResolveAnswerBlock(questionnaire, block); err == nil {
		t.Fatal("expected changed question to be rejected")
	}
}

func TestResolveAnswerBlockRejectsChangedOption(t *testing.T) {
	questionnaire := Questionnaire{Questions: []Question{
		{ID: "q1", Text: "Choose", Kind: QuestionSingle, Options: []QuestionOption{{ID: "a", Text: "New option"}}},
	}}
	block := AnswerBlock{
		Tag: "old-set", Name: "Old set", Kind: AnswerBlockQualification, Platform: "mock",
		Answers: []StoredAnswer{{Question: "Choose", SelectedOptions: []string{"Old option"}}},
	}

	if _, err := ResolveAnswerBlock(questionnaire, block); err == nil {
		t.Fatal("expected changed option to be rejected")
	}
}
