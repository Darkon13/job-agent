package main

import (
	"os"

	browserstate "github.com/Darkon13/job-agent/internal/tools/browserstate"
)

func main() { os.Exit(browserstate.Run(os.Args[1:])) }
