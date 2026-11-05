package hh

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCaptchaPendingGate(t *testing.T) {
	original := captchaPendingFilePath
	defer func() { captchaPendingFilePath = original }()
	captchaPendingFilePath = filepath.Join(t.TempDir(), "captcha-pending.txt")
	if err := os.WriteFile(captchaPendingFilePath, []byte("https://hh.ru/account/captcha?state=x"), 0o600); err != nil {
		t.Fatalf("write pending captcha: %v", err)
	}
	if retryAt, pending := captchaPending(); !pending || !retryAt.After(time.Now()) {
		t.Fatalf("fresh captcha was not detected: retryAt=%v pending=%t", retryAt, pending)
	}
	stale := time.Now().Add(-captchaPendingMaxAge - time.Minute)
	if err := os.Chtimes(captchaPendingFilePath, stale, stale); err != nil {
		t.Fatalf("age pending captcha: %v", err)
	}
	if _, pending := captchaPending(); pending {
		t.Fatal("stale captcha must not block submissions forever")
	}
}
