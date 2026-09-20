package main

import (
	"os"

	check "github.com/Darkon13/job-agent/internal/tools/check"
)

func main() { os.Exit(check.Run(os.Args[1:])) }
