package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRecord() Record {
	expires := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	return Record{
		Platform: "hh", ProfileID: "primary", TokenType: "bearer",
		AccessToken: "access-secret", RefreshToken: "refresh-secret",
		ExpiresAt: &expires, Scopes: []string{"applications"}, Revision: 3,
	}
}

func TestWriteAndLoadJSONRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	record := testRecord()
	if err := WriteFile(path, record, FormatJSON, false); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o, want 600", info.Mode().Perm())
	}
	loaded, err := Load("file:" + path)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}
	if loaded.AccessToken != record.AccessToken || loaded.RefreshToken != record.RefreshToken ||
		loaded.Platform != record.Platform || loaded.ProfileID != record.ProfileID ||
		loaded.TokenType != record.TokenType || loaded.Revision != record.Revision ||
		loaded.ExpiresAt == nil || !loaded.ExpiresAt.Equal(*record.ExpiresAt) ||
		strings.Join(loaded.Scopes, ",") != "applications" {
		t.Fatalf("loaded = %#v", loaded)
	}
}

func TestWriteAndLoadDotenvRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.env")
	record := testRecord()
	if err := WriteFile(path, record, FormatDotenv, false); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if strings.Contains(string(data), "access-secret") == false || strings.Contains(string(data), `'`) == false {
		t.Fatalf("dotenv content = %q", data)
	}
	loaded, err := Load("dotenv-file:" + path)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}
	if loaded.AccessToken != record.AccessToken || loaded.RefreshToken != record.RefreshToken ||
		loaded.ExpiresAt == nil || !loaded.ExpiresAt.Equal(*record.ExpiresAt) {
		t.Fatalf("loaded = %#v", loaded)
	}
}

func TestLoadBarePathKeepsWorkingAsJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := WriteFile(path, testRecord(), FormatJSON, false); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("load bare path: %v", err)
	}
}

func TestWriteFileRefusesOverwriteWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := WriteFile(path, testRecord(), FormatJSON, false); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	updated := testRecord()
	updated.AccessToken = "second-secret"
	if err := WriteFile(path, updated, FormatJSON, false); !errors.Is(err, ErrSecretExists) {
		t.Fatalf("error = %v, want secret exists", err)
	}
	loaded, err := Load("file:" + path)
	if err != nil || loaded.AccessToken != "access-secret" {
		t.Fatalf("loaded = %#v err=%v", loaded, err)
	}
	if err := WriteFile(path, updated, FormatJSON, true); err != nil {
		t.Fatalf("force write: %v", err)
	}
	loaded, err = Load("file:" + path)
	if err != nil || loaded.AccessToken != "second-secret" {
		t.Fatalf("loaded after force = %#v err=%v", loaded, err)
	}
}

func TestWriteFileRefusesSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"access_token":"target-secret"}`), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(directory, "credentials.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := WriteFile(link, testRecord(), FormatJSON, true); !errors.Is(err, ErrSymlinkNotAllowed) {
		t.Fatalf("error = %v, want symlink rejected", err)
	}
	loaded, err := Load("file:" + link)
	if err != nil || loaded.AccessToken != "target-secret" {
		t.Fatalf("symlink target changed: %#v err=%v", loaded, err)
	}
}

func TestLoadFollowsSymlinkedSecretAndRejectsUnsafePermissions(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"access_token":"secret"}`), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(directory, "credentials.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := Load("file:" + link); err != nil {
		t.Fatalf("load symlinked secret: %v", err)
	}
	insecure := filepath.Join(directory, "insecure.json")
	if err := os.WriteFile(insecure, []byte(`{"access_token":"secret"}`), 0o644); err != nil {
		t.Fatalf("write insecure: %v", err)
	}
	if _, err := Load("file:" + insecure); !errors.Is(err, ErrUnsafePermissions) {
		t.Fatalf("error = %v, want unsafe permissions", err)
	}
}

func TestLoadRejectsInvalidRecords(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing.json")
	if _, err := Load("file:" + missing); !errors.Is(err, ErrReferenceUnavailable) {
		t.Fatalf("missing file error = %v", err)
	}
	empty := filepath.Join(directory, "empty.json")
	if err := os.WriteFile(empty, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if _, err := Load("file:" + empty); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("empty record error = %v", err)
	}
	badJSON := filepath.Join(directory, "bad.json")
	if err := os.WriteFile(badJSON, []byte(`{"access_token":`), 0o600); err != nil {
		t.Fatalf("write bad json: %v", err)
	}
	if _, err := Load("file:" + badJSON); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("bad json error = %v", err)
	}
}

func TestParseReference(t *testing.T) {
	tests := []struct {
		value  string
		scheme string
		path   string
	}{
		{value: "file:/run/secrets/hh.json", scheme: "file", path: "/run/secrets/hh.json"},
		{value: "dotenv-file:/run/secrets/hh.env", scheme: "dotenv-file", path: "/run/secrets/hh.env"},
		{value: "/run/secrets/hh.json", scheme: "file", path: "/run/secrets/hh.json"},
	}
	for _, test := range tests {
		reference, err := ParseReference(test.value)
		if err != nil || reference.Scheme != test.scheme || reference.Path != test.path {
			t.Fatalf("ParseReference(%q) = %#v err=%v", test.value, reference, err)
		}
	}
	for _, value := range []string{"", "vault://secret", "file:", "dotenv-file: "} {
		if _, err := ParseReference(value); !errors.Is(err, ErrUnsupportedReference) {
			t.Fatalf("ParseReference(%q) error = %v", value, err)
		}
	}
}

func TestDotenvHelpersRejectUnsafeValues(t *testing.T) {
	record := testRecord()
	record.AccessToken = "value'with'quote"
	if _, err := Encode(record, FormatDotenv); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("encode error = %v", err)
	}
	if _, err := Encode(record, "yaml"); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("unknown format error = %v", err)
	}
	if _, err := ParseDotenv([]byte("HH_ACCESS_TOKEN='secret'\nHH_EXPIRES_AT='tomorrow'\n")); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("invalid expiry error = %v", err)
	}
	if _, err := ParseDotenv([]byte("HH_ACCESS_TOKEN='secret'\nnot-a-pair\n")); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("invalid line error = %v", err)
	}
}
