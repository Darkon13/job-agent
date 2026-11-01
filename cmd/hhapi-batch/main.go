package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
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
		WHERE profile_id='main' AND status IN ('ready','new') AND prepared_message!=''
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

	sent, skipped, failed := 0, 0, 0
	for _, c := range list {
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
		time.Sleep(time.Duration(1000+rand.Intn(2000)) * time.Millisecond)
	}
	fmt.Printf("done: sent=%d skipped=%d failed=%d\n", sent, skipped, failed)
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
