package main

import (
	"os"

	trigger "github.com/Darkon13/job-agent/internal/tools/trigger"
)

func main() { os.Exit(trigger.Run(os.Args[1:])) }
