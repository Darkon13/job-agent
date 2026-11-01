package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
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
		restore := tailor(client, c.id, *resume)
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
		restore()
		time.Sleep(time.Duration(45+rand.Intn(46)) * time.Second)
	}
	fmt.Printf("done: sent=%d skipped=%d failed=%d\n", sent, skipped, failed)
}

// tailor добавляет навыки вакансии в резюме и возвращает функцию восстановления.
func tailor(client *hhapi.Client, vacancyID, resumeID string) func() {
	ctx := context.Background()
	baseline, err := client.Resume(ctx, resumeID)
	if err != nil {
		return func() {}
	}
	vacancy, err := client.Vacancy(ctx, vacancyID)
	if err != nil {
		return func() {}
	}
	seen := map[string]bool{}
	var tags []string
	for _, skill := range vacancy.KeySkills {
		name := strings.TrimSpace(skill.Name)
		if name != "" && !seen[strings.ToLower(name)] {
			seen[strings.ToLower(name)] = true
			tags = append(tags, name)
		}
	}
	for _, name := range baseline.SkillSet {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		tags = append(tags, name)
	}
	if len(tags) > 30 {
		tags = tags[:30]
	}
	text := baseline.Skills
	if len(vacancy.KeySkills) > 0 {
		var names []string
		for _, skill := range vacancy.KeySkills {
			names = append(names, skill.Name)
		}
		text = strings.TrimRight(text, " \n") + "\n\nПод вакансию «" + vacancy.Name + "»: " + strings.Join(names, ", ")
	}
	patch := hhapi.ResumePatch{Skills: &text, SkillSet: &tags}
	if err := client.UpdateResume(ctx, resumeID, patch); err != nil {
		return func() {}
	}
	fmt.Printf("TAILOR %s tags=%d\n", vacancyID, len(tags))
	return func() {
		_ = client.UpdateResume(ctx, resumeID, hhapi.ResumePatch{Skills: &baseline.Skills, SkillSet: &baseline.SkillSet})
	}
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
