package worker

import (
	"strings"
)

// ChatReplyDecision описывает, нужно ли автоматически отвечать на сообщение.
type ChatReplyDecision struct {
	Reply  bool
	Reason string
}

var chatDisqualifying = []string{
	"переезд", "релокац", "переехать", "гражданств", "виз", "разрешение на работу",
	"водительск", "права категории", "проживаете в", "офис", "гибрид", "5/2",
	"удаленн", "удалённ", "удаленк", "удалёнк", "remote", "формат работы",
	"график работы", "сменн", "ночн",
	"командиров", "испытательн", "самозанят", "оформлен", "трудоустрой",
	"военн", "судим", "телефон", "контакт", "почта", "почты", "почту", "почте", "почтов", "email", "e-mail",
	"telegram", "телеграм",
}

var chatQuestionMarkers = []string{
	"?", "уточните", "расскажите", "есть ли", "готовы ли", "готовы", "рассматриваете",
	"какой опыт", "какие технологии", "ожидания", "сколько", "когда можете",
}

// DecideChatReply отвечает только на вопросы и никогда — на дисквалифицирующие темы.
func DecideChatReply(message string) ChatReplyDecision {
	text := strings.ToLower(strings.TrimSpace(message))
	if text == "" {
		return ChatReplyDecision{Reason: "empty"}
	}
	for _, term := range chatDisqualifying {
		if strings.Contains(text, term) {
			return ChatReplyDecision{Reason: "disqualifying:" + term}
		}
	}
	for _, marker := range chatQuestionMarkers {
		if strings.Contains(text, marker) {
			return ChatReplyDecision{Reply: true, Reason: "question"}
		}
	}
	return ChatReplyDecision{Reason: "not-a-question"}
}
