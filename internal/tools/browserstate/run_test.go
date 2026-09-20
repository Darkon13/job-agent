package browserstate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSanitizesStateWithoutPrintingValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"cookies":[{"name":"session","value":"secret","domain":".hh.ru","path":"/"}],"origins":[]}`), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	var output bytes.Buffer
	if err := run([]string{"sanitize", path}, &output); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(output.String(), "cookies=1/1") || strings.Contains(output.String(), "secret") {
		t.Fatalf("unsafe output: %q", output.String())
	}
}
