package hhapi

import (
	"context"
	"fmt"
	"strings"
)

// ChatReply генерирует ответ работодателю по фактам; для вопросов о деньгах
// подставляет вилку: сначала из вакансии, иначе медиану похожих.
func (c *Client) ChatReply(ctx context.Context, apiKey, baseURL, model, facts, vacancyID, question string) (string, error) {
	text := strings.ToLower(question)
	salaryHint := ""
	if strings.Contains(text, "зарплат") || strings.Contains(text, "ожидан") || strings.Contains(text, "доход") {
		if low, high, err := c.SalaryRange(ctx, vacancyID); err == nil {
			salaryHint = fmt.Sprintf("Ориентир по зарплате: %d–%d ₽, торг уместен.", low, high)
		}
	}
	prompt := "Ответь работодателю в чате HH, по-русски, кратко (1–3 предложения), от первого лица. " +
		"Используй только факты кандидата, ничего не выдумывай. Не упоминай пет-проекты и учебные задачи. " +
		salaryHint + "\n\nФАКТЫ:\n" + facts + "\n\nВОПРОС РАБОТОДАТЕЛЯ:\n" + question
	return GenerateText(ctx, apiKey, baseURL, model, prompt)
}
