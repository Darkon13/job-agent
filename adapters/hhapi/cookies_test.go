package hhapi

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testJar() *persistentCookieJar {
	jar := newPersistentCookieJar()
	hh, _ := url.Parse("https://hh.ru/")
	api, _ := url.Parse("https://api.hh.ru/")
	jar.SetCookies(hh, []*http.Cookie{
		{Name: "__ddg1_", Value: "device", Domain: ".hh.ru", Path: "/", Secure: true, Expires: time.Now().Add(time.Hour)},
		{Name: "hhtoken", Value: "token", Domain: ".hh.ru", Path: "/", Secure: true},
	})
	jar.SetCookies(api, []*http.Cookie{
		{Name: "api_only", Value: "value", Domain: "api.hh.ru", Path: "/"},
	})
	return jar
}

func TestCookieJarRoundTrip(t *testing.T) {
	jar := testJar()
	exported := jar.export()
	if len(exported) != 3 {
		t.Fatalf("exported cookies = %#v", exported)
	}
	path := filepath.Join(t.TempDir(), "nested", "hh-cookies.txt")
	if err := saveCookieJar(path, jar); err != nil {
		t.Fatalf("save cookies: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cookie file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cookie file mode = %o", info.Mode().Perm())
	}
	loaded := loadCookieJar(path)
	values := map[string]string{}
	for _, cookie := range loaded.export() {
		values[cookie.Name] = cookie.Value
	}
	if values["__ddg1_"] != "device" || values["hhtoken"] != "token" || values["api_only"] != "value" {
		t.Fatalf("loaded cookies = %#v", values)
	}
}

func TestMergeCookieJarKeepsSourceCookies(t *testing.T) {
	destination := testJar()
	source := newPersistentCookieJar()
	hh, _ := url.Parse("https://hh.ru/")
	source.SetCookies(hh, []*http.Cookie{
		{Name: "new_cookie", Value: "fresh", Domain: ".hh.ru", Path: "/"},
	})
	mergeCookieJar(destination, source)
	found := false
	for _, cookie := range destination.export() {
		if cookie.Name == "new_cookie" && cookie.Value == "fresh" {
			found = true
		}
	}
	if !found {
		t.Fatalf("merged cookies = %#v", destination.export())
	}
}

func TestClientSharesCookiesThroughFile(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "hh-cookies.txt")
	t.Setenv("HH_COOKIE_FILE", cookieFile)
	tokenFile := filepath.Join(dir, "token.json")
	if err := os.WriteFile(tokenFile, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	first, err := New(tokenFile)
	if err != nil {
		t.Fatalf("new first client: %v", err)
	}
	hh, _ := url.Parse("https://hh.ru/")
	first.cookies.SetCookies(hh, []*http.Cookie{{Name: "device", Value: "identity", Domain: ".hh.ru", Path: "/"}})
	first.saveCookies(true)

	second, err := New(tokenFile)
	if err != nil {
		t.Fatalf("new second client: %v", err)
	}
	found := false
	for _, cookie := range second.cookies.Cookies(hh) {
		if cookie.Name == "device" && cookie.Value == "identity" {
			found = true
		}
	}
	if !found {
		t.Fatalf("second client did not load the shared cookie")
	}
}
