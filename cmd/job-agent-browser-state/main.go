package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/Darkon13/job-agent/adapters/hh"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, output io.Writer) error {
	if len(args) != 2 || args[0] != "sanitize" {
		return errors.New("usage: job-agent-browser-state sanitize <state.json>")
	}
	result, err := hh.SanitizeBrowserStorageState(args[1])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "OK browser_state cookies=%d/%d origins=%d/%d mode=0600\n",
		result.CookiesAfter, result.CookiesBefore, result.OriginsAfter, result.OriginsBefore)
	return err
}
