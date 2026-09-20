package browserstate

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/Darkon13/job-agent/adapters/hh"
	"github.com/Darkon13/job-agent/buildinfo"
)

func runMain() {
	if buildinfo.Requested(os.Args[1:]) {
		if err := buildinfo.Write("job-agent-browser-state", os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
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

// Run executes the tool with the given arguments and returns a process exit
// code. It preserves the historical CLI contract of cmd/job-agent-browser-state.
func Run(args []string) int {
	os.Args = append([]string{os.Args[0]}, args...)
	runMain()
	return 0
}
