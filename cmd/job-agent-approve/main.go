package main

import (
	"os"

	approve "github.com/Darkon13/job-agent/internal/tools/approve"
)

func main() { os.Exit(approve.Run(os.Args[1:])) }
