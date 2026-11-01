package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/Darkon13/job-agent/adapters/hhapi"
)

func main() {
	vacancy := flag.String("vacancy", "", "id вакансии")
	resume := flag.String("resume", "", "id резюме")
	messageFile := flag.String("message", "", "файл сопроводительного")
	tokenFile := flag.String("token", "", "файл токена")
	flag.Parse()
	if *vacancy == "" || *resume == "" || *tokenFile == "" {
		log.Fatal("usage: hhapi-apply -vacancy ID -resume ID -message FILE -token FILE")
	}
	message := ""
	if *messageFile != "" {
		raw, err := os.ReadFile(*messageFile)
		if err != nil {
			log.Fatal(err)
		}
		message = string(raw)
	}
	client, err := hhapi.New(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	request := hhapi.ApplyRequest{VacancyID: *vacancy, ResumeID: *resume, Message: message}
	for attempt := 0; attempt < 6; attempt++ {
		negotiation, err := client.Apply(ctx, request)
		if err == nil {
			fmt.Println("OK negotiation:", negotiation.ID)
			return
		}
		var captcha *hhapi.CaptchaRequiredError
		if !errors.As(err, &captcha) {
			log.Fatal(err)
		}
		fmt.Println("CAPTCHA:", captcha.URL)
		err = hhapi.SolveCaptcha(ctx, captcha.URL, func(image []byte) (string, error) {
			if err := os.WriteFile("/tmp/hh-captcha.png", image, 0o600); err != nil {
				return "", err
			}
			fmt.Println("IMAGE:/tmp/hh-captcha.png")
			return bufio.NewReader(os.Stdin).ReadString('\n')
		})
		if err != nil {
			log.Fatal(err)
		}
	}
	log.Fatal("captcha attempts exhausted")
}
