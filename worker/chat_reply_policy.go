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
	"военн", "судим", "телефон", "контакт", "почта", "почты", "почту", "почте",
	"почтов", "email", "e-mail", "telegram", "телеграм", "фио", "возраст",
	"дата рождения", "тг ",
}

var chatRequestMarkers = []string{
	"уточните", "расскажите", "напишите", "сообщите", "опишите", "перечислите",
	"подскажите", "не могли бы вы", "хотелось бы узнать", "интересует",
}

var chatInterrogativeStarts = []string{
	"какой", "какая", "какие", "какое", "каков", "где", "когда", "почему",
	"зачем", "сколько", "есть ли", "готовы ли", "рассматриваете ли",
	"можете ли", "хотите ли", "планируете ли", "хотели бы", "будете ли",
}

// DecideChatReply отвечает только на вопросы и никогда — на дисквалифицирующие
// темы или сообщения с контактами. Вопросом считается вопросительный знак,
// явная просьба («расскажите», «уточните») или предложение, начинающееся с
// вопросительного слова. Третье лицо («мы готовы рассматривать») не является
// вопросом, даже если содержит слово «готовы».
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
	if strings.Contains(text, "@") || strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		return ChatReplyDecision{Reason: "contact-link"}
	}
	if strings.Contains(text, "?") {
		return ChatReplyDecision{Reply: true, Reason: "question-mark"}
	}
	for _, marker := range chatRequestMarkers {
		if strings.Contains(text, marker) {
			return ChatReplyDecision{Reply: true, Reason: "request:" + marker}
		}
	}
	for _, segment := range chatSentences(text) {
		for _, start := range chatInterrogativeStarts {
			if strings.HasPrefix(segment, start) {
				return ChatReplyDecision{Reply: true, Reason: "interrogative:" + start}
			}
		}
	}
	return ChatReplyDecision{Reason: "not-a-question"}
}

func chatSentences(text string) []string {
	segments := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case '.', '!', '\n', '?', ';':
			return true
		default:
			return false
		}
	})
	result := make([]string, 0, len(segments))
	for _, segment := range segments {
		if trimmed := strings.TrimSpace(segment); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
