package core

import "testing"

func TestAnswerBlockRegistrySeparatesQualificationAndConversationBlocks(t *testing.T) {
	qualification := AnswerBlock{
		Tag: "hh-go-basics", Name: "HH Go: основы", Kind: AnswerBlockQualification, Platform: "hh",
		Qualification: &QualificationDescriptor{FamilyID: "go", FamilyName: "Go", LevelID: "basic", LevelName: "Базовый"},
		Answers:       []StoredAnswer{{Question: "Какой вариант?", SelectedOptions: []string{"Первый"}}},
	}
	conversation := AnswerBlock{
		Tag: "hh-chat-salary", Name: "Ожидания по зарплате", Kind: AnswerBlockConversation, Platform: "hh",
		Match:   AnswerBlockMatcher{Topic: "salary expectations"},
		Answers: []StoredAnswer{{Question: "Ваши ожидания?", Text: "Ответ из политики профиля"}},
	}
	registry, err := NewAnswerBlockRegistry(qualification, conversation)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	got, exists := registry.FindQualificationLevel("hh", "go", "basic")
	if !exists || got.Tag != qualification.Tag {
		t.Fatalf("qualification level block not found: %#v", got)
	}
	got, exists = registry.FindConversationTopic("hh", "  SALARY   expectations ")
	if !exists || got.Tag != conversation.Tag {
		t.Fatalf("conversation block not found: %#v", got)
	}
	got.Answers[0].Text = "mutated"
	again, exists := registry.Get(conversation.Tag)
	if !exists || again.Answers[0].Text == "mutated" {
		t.Fatal("registry exposed mutable internal data")
	}
}

func TestAnswerBlockRegistryRejectsAmbiguousMatchers(t *testing.T) {
	first := AnswerBlock{
		Tag: "first", Name: "First", Kind: AnswerBlockQualification, Platform: "hh",
		Qualification: &QualificationDescriptor{FamilyID: "go", FamilyName: "Go", LevelID: "basic", LevelName: "Базовый"},
		Answers:       []StoredAnswer{{Question: "Question", Text: "Answer"}},
	}
	second := first
	second.Tag = "second"
	second.Name = "Second"
	if _, err := NewAnswerBlockRegistry(first, second); err == nil {
		t.Fatal("expected duplicate qualification matcher to fail")
	}
}
