package hh

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestQualificationBrowserExchangesSequentialCommands(t *testing.T) {
	worker := filepath.Join(t.TempDir(), "worker.sh")
	script := `#!/bin/sh
index=0
while IFS= read -r line; do
  index=$((index + 1))
  case "$index" in
    1) printf '%s\n' '{"id":1,"ok":true,"offering":{"skill_id":"510338","family_name":"Docker","level":"Базовый","kind":"theory","start_available":true}}' ;;
    2) printf '%s\n' '{"id":2,"ok":true,"capture":{"status":"question","question":{"id":"runtime-1","text":"Question?","kind":"single","options":[{"id":"runtime-a","text":"Answer"}]},"progress":{"current":1,"total":10}}}' ;;
    3) printf '%s\n' '{"id":3,"ok":true,"capture":{"status":"question","question":{"id":"runtime-1","text":"Question?","kind":"single","options":[{"id":"runtime-a","text":"Answer"}]},"selected_option_ids":["runtime-a"],"progress":{"current":1,"total":10}}}' ;;
    4) printf '%s\n' '{"id":4,"ok":true,"capture":{"status":"completed"}}' ;;
  esac
done
`
	if err := os.WriteFile(worker, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake worker: %v", err)
	}
	browser, err := StartQualificationBrowser(context.Background(), QualificationBrowserOptions{
		NodePath: "/bin/sh", WorkerPath: worker, BrowserPath: "ignored", StateFile: "ignored",
	})
	if err != nil {
		t.Fatalf("start browser: %v", err)
	}
	t.Cleanup(func() { _ = browser.Close() })
	offering, err := browser.OpenOffering(context.Background(), "510338", "Базовый", "theory")
	if err != nil || !offering.StartAvailable {
		t.Fatalf("open offering: %#v, %v", offering, err)
	}
	capture, err := browser.StartAttempt(context.Background())
	if err != nil || capture.Question.Text != "Question?" {
		t.Fatalf("start attempt: %#v, %v", capture, err)
	}
	capture, err = browser.Select(context.Background(), capture.Question.Text, []string{"runtime-a"})
	if err != nil || len(capture.SelectedOptionIDs) != 1 {
		t.Fatalf("select: %#v, %v", capture, err)
	}
	capture, err = browser.Next(context.Background(), capture.Question.Text, []string{"runtime-a"})
	if err != nil || !capture.Completed() {
		t.Fatalf("next: %#v, %v", capture, err)
	}
}
