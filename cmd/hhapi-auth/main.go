package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Darkon13/job-agent/adapters/hhapi"
)

func main() {
	user := flag.String("user", "", "email или телефон")
	password := flag.String("password", "", "пароль (пусто — вход по OTP)")
	tokenFile := flag.String("token", "hh-token.json", "файл токена")
	flag.Parse()
	if *user == "" {
		log.Fatal("usage: hhapi-auth -user EMAIL [-password PASS] [-token FILE]")
	}
	ctx := context.Background()
	client, err := hhapi.New(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	session, err := client.BeginLogin(ctx, hhapi.AndroidClientID)
	if err != nil {
		log.Fatal(err)
	}
	if *password != "" {
		if err := session.LoginPassword(ctx, *user, *password); err != nil {
			log.Fatal(err)
		}
	} else {
		isEmail := strings.Contains(*user, "@")
		if err := session.RequestOTP(ctx, *user, isEmail); err != nil {
			log.Fatal(err)
		}
		fmt.Print("Код из SMS/email: ")
		code, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if err := session.ConfirmOTP(ctx, *user, strings.TrimSpace(code)); err != nil {
			log.Fatal(err)
		}
	}
	if err := session.FinishOAuth(ctx, client, hhapi.AndroidClientID, hhapi.AndroidClientSecret); err != nil {
		log.Fatal(err)
	}
	var me struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	if err := client.Request(ctx, "GET", "/me", nil, &me); err != nil {
		log.Fatalf("авторизация прошла, но /me не ответил: %v", err)
	}
	fmt.Printf("OK: %s %s, токен сохранён в %s\n", me.FirstName, me.LastName, *tokenFile)
}
