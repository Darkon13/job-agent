package core

import (
	"strings"
	"time"
)

// ConversationAnswerDecision is one reviewed answer the adapter may send for a
// questionnaire prompt that appeared in a conversation. Options carry the
// platform option id and the exact button text; nothing else leaves the
// resolver.
type ConversationAnswerDecision struct {
	PromptMessageID  MessageID
	PromptExternalID string
	AnswerBlockTag   string
	Option           MessageOption
}

// ResolveKnownConversationAnswer returns the earliest incoming questionnaire
// prompt that is still unanswered and whose question has a reviewed
// conversation answer. It never guesses: a missing, stale or ambiguous answer
// yields ok=false so the prompt stays for a human.
func ResolveKnownConversationAnswer(platform Platform, messages []ConversationMessage, registry *AnswerBlockRegistry) (ConversationAnswerDecision, bool) {
	if registry == nil || platform == "" {
		return ConversationAnswerDecision{}, false
	}
	var lastOutgoing time.Time
	for _, message := range messages {
		if message.Direction != MessageOutgoing {
			continue
		}
		if message.Status != MessageSent && message.Status != MessageQueued {
			continue
		}
		if message.OccurredAt.After(lastOutgoing) {
			lastOutgoing = message.OccurredAt
		}
	}
	var prompt *ConversationMessage
	for index := range messages {
		message := messages[index]
		if message.Direction != MessageIncoming || message.Kind != MessageQuestionnaire || len(message.Options) == 0 {
			continue
		}
		if !message.OccurredAt.After(lastOutgoing) {
			continue
		}
		if prompt == nil || message.OccurredAt.Before(prompt.OccurredAt) {
			candidate := message
			prompt = &candidate
		}
	}
	if prompt == nil {
		return ConversationAnswerDecision{}, false
	}
	block, found := conversationAnswerBlock(platform, *prompt, registry)
	if !found {
		return ConversationAnswerDecision{}, false
	}
	answer, found := conversationStoredAnswer(block, *prompt)
	if !found || strings.TrimSpace(answer.Text) != "" || len(answer.SelectedOptions) != 1 {
		return ConversationAnswerDecision{}, false
	}
	option, found := conversationOption(prompt.Options, answer.SelectedOptions[0])
	if !found {
		// The offered set changed after the answer was reviewed; the prompt
		// must not receive a stale label as free text.
		return ConversationAnswerDecision{}, false
	}
	return ConversationAnswerDecision{
		PromptMessageID: prompt.ID, PromptExternalID: prompt.ExternalID,
		AnswerBlockTag: block.Tag, Option: option,
	}, true
}

func conversationAnswerBlock(platform Platform, prompt ConversationMessage, registry *AnswerBlockRegistry) (AnswerBlock, bool) {
	if block, found := registry.FindConversationTopic(platform, prompt.Text); found {
		return block, true
	}
	fingerprint, ok := conversationPromptFingerprint(prompt)
	if !ok {
		return AnswerBlock{}, false
	}
	return registry.FindConversationFingerprint(platform, fingerprint)
}

func conversationPromptFingerprint(prompt ConversationMessage) (string, bool) {
	options := make([]QuestionOption, 0, len(prompt.Options))
	for _, option := range prompt.Options {
		options = append(options, QuestionOption{ID: option.ID, Text: option.Text})
	}
	id := strings.TrimSpace(prompt.ExternalID)
	if id == "" {
		id = "conversation-prompt"
	}
	fingerprint, err := QuestionFingerprint(Question{ID: id, Text: prompt.Text, Kind: QuestionSingle, Options: options})
	if err != nil {
		return "", false
	}
	return fingerprint, true
}

func conversationStoredAnswer(block AnswerBlock, prompt ConversationMessage) (StoredAnswer, bool) {
	key := NormalizeQuestionText(prompt.Text)
	fingerprint, hasFingerprint := conversationPromptFingerprint(prompt)
	for _, answer := range block.Answers {
		if NormalizeQuestionText(answer.Question) != key {
			continue
		}
		if answer.QuestionFingerprint != "" {
			if !hasFingerprint || answer.QuestionFingerprint != fingerprint {
				continue
			}
		}
		return answer, true
	}
	if !hasFingerprint {
		return StoredAnswer{}, false
	}
	for _, answer := range block.Answers {
		if answer.QuestionFingerprint == fingerprint {
			return answer, true
		}
	}
	return StoredAnswer{}, false
}

func conversationOption(options []MessageOption, selected string) (MessageOption, bool) {
	key := NormalizeQuestionText(selected)
	var match MessageOption
	matches := 0
	for _, option := range options {
		if NormalizeQuestionText(option.Text) != key {
			continue
		}
		match = option
		matches++
	}
	return match, matches == 1
}
