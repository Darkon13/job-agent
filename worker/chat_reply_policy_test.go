package worker

import "testing"

func TestDecideChatReply(t *testing.T) {
	tests := []struct {
		message string
		reply   bool
		reason  string
	}{
		{"Какие технологии использовали?", true, "question"},
		{"Готовы к тестовому?", true, "question"},
		{"Готовы к переезду в Москву?", false, "disqualifying:переезд"},
		{"Рассматриваете ли вы удалённый формат работы?", false, "disqualifying:удалённ"},
		{"Готовы ли вы к оформлению по ТК?", false, "disqualifying:оформлен"},
		{"Можете прислать телефон для связи?", false, "disqualifying:телефон"},
		{"Спасибо, всё понятно", false, "not-a-question"},
	}
	for _, test := range tests {
		decision := DecideChatReply(test.message)
		if decision.Reply != test.reply || decision.Reason != test.reason {
			t.Fatalf("%q: got %+v, want reply=%v reason=%q", test.message, decision, test.reply, test.reason)
		}
	}
}
