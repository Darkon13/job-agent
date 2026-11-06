package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Darkon13/job-agent/adapters/hhapi"
	_ "modernc.org/sqlite"
)

func main() {
	tokenFile := flag.String("token", "", "файл токена")
	dbPath := flag.String("db", "data/job-agent.db", "база job-agent")
	limit := flag.Int("limit", 20, "сколько откликов отправить")
	resume := flag.String("resume", "d1f2239dff10df38800039ed1f3539504b3944", "id резюме")
	flag.Parse()
	if *tokenFile == "" {
		log.Fatal("usage: hhapi-batch -token FILE [-db DB] [-limit N]")
	}
	client, err := hhapi.New(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT external_id, prepared_message FROM applications
		WHERE profile_id='main' AND status IN ('ready','new')
		ORDER BY updated_at DESC LIMIT ?`, *limit)
	if err != nil {
		log.Fatal(err)
	}
	type candidate struct{ id, message string }
	var list []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.message); err != nil {
			log.Fatal(err)
		}
		list = append(list, c)
	}
	rows.Close()

	facts, _ := os.ReadFile("deploy/facts/main.json")
	apiKey := os.Getenv("OPENCODE_API_KEY")
	if apiKey == "" {
		if env, err := os.ReadFile(".env"); err == nil {
			for _, line := range strings.Split(string(env), "\n") {
				if strings.HasPrefix(line, "OPENCODE_API_KEY=") {
					apiKey = strings.TrimSpace(strings.TrimPrefix(line, "OPENCODE_API_KEY="))
				}
			}
		}
	}
	sent, skipped, failed := 0, 0, 0
	for _, c := range list {
		vacancy, vacErr := client.Vacancy(context.Background(), c.id)
		if vacErr == nil && vacancy.BlockedRole() {
			_, _ = db.Exec(`UPDATE applications SET status='skipped', decision_code='role_blocked', decision_reason='роль вне профиля', updated_at=? WHERE profile_id='main' AND external_id=?`, time.Now().UnixNano(), c.id)
			skipped++
			fmt.Printf("SKIP %s role_blocked\n", c.id)
			continue
		}
		if vacErr == nil && vacancy.BlockedCompany() {
			_, _ = db.Exec(`UPDATE applications SET status='skipped', decision_code='company_blocked', decision_reason='компания в чёрном списке', updated_at=? WHERE profile_id='main' AND external_id=?`, time.Now().UnixNano(), c.id)
			skipped++
			fmt.Printf("SKIP %s company_blocked\n", c.id)
			continue
		}
		if vacErr == nil && c.message == "" && apiKey != "" {
			promptTemplate := loadPrompt()
			prompt := fmt.Sprintf(promptTemplate, string(facts), vacancy.Name, vacancy.Employer.Name, truncate(vacancy.Description, 4000))
			if letter, genErr := hhapi.GenerateText(context.Background(), apiKey, "https://opencode.ai/inference/go/openai/v1", "deepseek-v4.1-flash", prompt); genErr == nil {
				c.message = letter
				_, _ = db.Exec(`UPDATE applications SET prepared_message=?, prepared_at=?, updated_at=? WHERE profile_id='main' AND external_id=?`, letter, time.Now().UnixNano(), time.Now().UnixNano(), c.id)
				fmt.Printf("LETTER %s %d\n", c.id, len(letter))
			} else {
				fmt.Printf("LETTER_FAIL %s %v\n", c.id, genErr)
			}
		}
		if vacErr == nil && !vacancy.Remote() {
			_, _ = db.Exec(`UPDATE applications SET status='skipped', decision_code='not_remote', decision_reason='вакансия без удалёнки', updated_at=? WHERE profile_id='main' AND external_id=?`, time.Now().UnixNano(), c.id)
			skipped++
			fmt.Printf("SKIP %s not_remote\n", c.id)
			continue
		}
		if strings.TrimSpace(c.message) == "" {
			fmt.Printf("SKIP %s no_letter\n", c.id)
			continue
		}
		result, err := client.Apply(context.Background(), hhapi.ApplyRequest{
			VacancyID: c.id, ResumeID: *resume, Message: c.message,
		})
		now := time.Now().UnixNano()
		if err == nil {
			_, _ = db.Exec(`UPDATE applications SET status='submitted', submitted_at=?, updated_at=?, external_negotiation_id=?, failure_category='', failure_message='' WHERE profile_id='main' AND external_id=?`,
				now, now, result.ID, c.id)
			sent++
			fmt.Printf("OK %s\n", c.id)
		} else {
			var captcha *hhapi.CaptchaRequiredError
			if errors.As(err, &captcha) {
				_ = os.WriteFile("data/captcha-pending.txt", []byte(captcha.URL), 0o600)
				fmt.Printf("CAPTCHA:%s\n", captcha.URL)
				fmt.Println("Реши: go run ./cmd/hhapi-captcha -url '<URL>' и запусти батч снова")
				return
			}
			if strings.Contains(err.Error(), "test_required") || strings.Contains(err.Error(), "questionnaire") || strings.Contains(err.Error(), "Must process test") {
				if answered := answerQuestionnaire(client, apiKey, string(facts), workspaceRoot(), c.id); answered {
					id := client.NegotiationID(context.Background(), c.id)
					_, _ = db.Exec(`UPDATE applications SET status='submitted', submitted_at=?, updated_at=?, external_negotiation_id=?, failure_category='', failure_message='' WHERE profile_id='main' AND external_id=?`, time.Now().UnixNano(), time.Now().UnixNano(), id, c.id)
					sent++
					fmt.Printf("OK %s questionnaire\n", c.id)
					continue
				}
			}
			state := classify(err)
			if state != "" {
				_, _ = db.Exec(`UPDATE applications SET status='skipped', decision_code=?, decision_reason=?, updated_at=? WHERE profile_id='main' AND external_id=?`,
					state, err.Error(), now, c.id)
				skipped++
				fmt.Printf("SKIP %s %s\n", c.id, state)
			} else {
				failed++
				fmt.Printf("FAIL %s %v\n", c.id, err)
			}
		}
		time.Sleep(time.Duration(1000+rand.Intn(2001)) * time.Millisecond)
	}
	fmt.Printf("done: sent=%d skipped=%d failed=%d\n", sent, skipped, failed)
}

func loadPrompt() string {
	if data, err := os.ReadFile("deploy/facts/letter-prompt.txt"); err == nil && len(data) > 0 {
		return string(data)
	}
	return "Сопроводительное письмо под вакансию: 1 вступление, 3–5 буллетов «— » с опытом из фактов, финал. Контакты кандидата — в начале, телефон не указывать.\n\nФАКТЫ:\n%s\n\nВАКАНСИЯ: %s (%s)\n\n%s"
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func classify(err error) string {
	var captcha *hhapi.CaptchaRequiredError
	if errors.As(err, &captcha) {
		return "captcha_required"
	}
	text := err.Error()
	for code, needle := range map[string]string{
		"already_applied":   "already_applied",
		"test_required":     "test_required",
		"questionnaire":     "questionnaire_required",
		"vacancy_not_found": "vacancy_not_found",
	} {
		if strings.Contains(text, needle) {
			return code
		}
	}
	if strings.Contains(text, "Must process test") {
		return "test_required"
	}
	if strings.Contains(text, "Already applied") {
		return "already_applied"
	}
	return ""
}

func workspaceRoot() string { return "." }

func workerToken() string {
	if value := strings.TrimSpace(os.Getenv("BROWSER_WORKER_TOKEN")); value != "" {
		return value
	}
	if env, err := os.ReadFile(".env"); err == nil {
		for _, line := range strings.Split(string(env), "\n") {
			if strings.HasPrefix(line, "BROWSER_WORKER_TOKEN=") {
				return strings.TrimSpace(strings.TrimPrefix(line, "BROWSER_WORKER_TOKEN="))
			}
		}
	}
	return ""
}

type workerFormField struct {
	Tag     string   `json:"tag"`
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Context string   `json:"context"`
	Options []string `json:"options"`
}
type workerForm struct {
	URL    string            `json:"url"`
	Fields []workerFormField `json:"fields"`
}
type workerAnswer struct {
	Selector string `json:"selector"`
	Value    string `json:"value"`
}
type workerAnswerResult struct {
	Confirmed bool `json:"confirmed"`
}

func postWorker(payload any, path string, out any) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", "http://127.0.0.1:8088/v1/profiles/main/"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+workerToken())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("worker %s: %d %s", path, resp.StatusCode, string(raw))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func answerQuestionnaire(client *hhapi.Client, apiKey, facts, root, vacancyID string) bool {
	var form workerForm
	if err := postWorker(map[string]string{"url": "https://hh.ru/vacancy/" + vacancyID}, "form", &form); err != nil {
		return false
	}
	var answers []workerAnswer
	for _, field := range form.Fields {
		if field.Name == "" {
			continue
		}
		question := strings.TrimSpace(field.Context + " " + field.Label)
		text := strings.ToLower(question)
		value := ""
		if field.Tag == "select" && len(field.Options) > 0 {
			prompt := question + " Варианты: " + strings.Join(field.Options, "; ")
			generated, err := hhapi.GenerateText(context.Background(), apiKey, "https://opencode.ai/inference/go/openai/v1", "deepseek-v4.1-flash", prompt+"\nОтветь только текстом одного варианта.")
			if err != nil {
				continue
			}
			for _, option := range field.Options {
				if strings.Contains(strings.ToLower(option), strings.ToLower(strings.TrimSpace(generated))) {
					value = option
					break
				}
			}
		} else if field.Tag == "textarea" || field.Type == "text" {
			if strings.Contains(text, "ожидан") || strings.Contains(text, "зарплат") {
				if low, high, err := client.SalaryRange(context.Background(), vacancyID); err == nil {
					value = fmt.Sprintf("Ожидания: %d–%d ₽, торг уместен.", low, high)
				}
			} else if apiKey != "" {
				prompt := "Ответь на вопрос анкеты по фактам кандидата, кратко.\nФАКТЫ:\n" + facts + "\nВОПРОС:\n" + question
				if generated, err := hhapi.GenerateText(context.Background(), apiKey, "https://opencode.ai/inference/go/openai/v1", "deepseek-v4.1-flash", prompt); err == nil {
					value = generated
				}
			}
		}
		if strings.TrimSpace(value) != "" {
			answers = append(answers, workerAnswer{Selector: fmt.Sprintf(`[name="%s"]`, field.Name), Value: value})
		}
	}
	if len(answers) == 0 {
		return false
	}
	var result workerAnswerResult
	if err := postWorker(map[string]any{"answers": answers, "submit": true}, "answers", &result); err != nil {
		return false
	}
	if result.Confirmed {
		return true
	}
	return client.NegotiationID(context.Background(), vacancyID) != ""
}
