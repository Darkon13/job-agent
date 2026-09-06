package hh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeBrowserStorageStateKeepsOnlyHHData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := rawBrowserStorageState{
		Cookies: []json.RawMessage{
			json.RawMessage(`{"name":"hh-session","value":"hh-secret","domain":".hh.ru","path":"/"}`),
			json.RawMessage(`{"name":"other-session","value":"other-secret","domain":".example.com","path":"/"}`),
		},
		Origins: []json.RawMessage{
			json.RawMessage(`{"origin":"https://hh.ru","localStorage":[{"name":"hh","value":"hh-local"}]}`),
			json.RawMessage(`{"origin":"https://example.com","localStorage":[{"name":"other","value":"other-local"}]}`),
		},
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	result, err := SanitizeBrowserStorageState(path)
	if err != nil {
		t.Fatalf("sanitize state: %v", err)
	}
	if result.CookiesBefore != 2 || result.CookiesAfter != 1 || result.OriginsBefore != 2 || result.OriginsAfter != 1 {
		t.Fatalf("result = %#v", result)
	}
	sanitized, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sanitized state: %v", err)
	}
	if strings.Contains(string(sanitized), "other-secret") || strings.Contains(string(sanitized), "other-local") {
		t.Fatal("sanitized state retained another domain")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sanitized state: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sanitized state mode = %v", info.Mode().Perm())
	}
}
