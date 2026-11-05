package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/Darkon13/job-agent/adapters/hhapi"
)

func main() {
	vacancy := flag.String("vacancy", "", "id вакансии")
	token := flag.String("token", "", "файл токена")
	flag.Parse()
	if *vacancy == "" || *token == "" {
		log.Fatal("usage: hhapi-salary -vacancy ID -token FILE")
	}
	client, err := hhapi.New(*token)
	if err != nil {
		log.Fatal(err)
	}
	low, high, err := client.SalaryRange(context.Background(), *vacancy)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Ожидания: %d–%d ₽, торг уместен.\n", low, high)
}
