package main

import (
	"os"

	questionbank "github.com/Darkon13/job-agent/internal/tools/questionbank"
)

func main() { os.Exit(questionbank.Run(os.Args[1:])) }
