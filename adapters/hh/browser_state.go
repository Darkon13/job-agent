package hh

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const maxBrowserStateSize = 8 << 20

type BrowserStateSanitizeResult struct {
	CookiesBefore int
	CookiesAfter  int
	OriginsBefore int
	OriginsAfter  int
}

type rawBrowserStorageState struct {
	Cookies []json.RawMessage `json:"cookies"`
	Origins []json.RawMessage `json:"origins"`
}

// SanitizeBrowserStorageState atomically narrows a shared Playwright export to
// HH-owned domains. Cookie values and local storage contents are never logged
// or returned to the caller.
func SanitizeBrowserStorageState(path string) (BrowserStateSanitizeResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return BrowserStateSanitizeResult{}, fmt.Errorf("inspect browser state: %w", err)
	}
	if !info.Mode().IsRegular() {
		return BrowserStateSanitizeResult{}, errors.New("browser state must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return BrowserStateSanitizeResult{}, fmt.Errorf("open browser state: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxBrowserStateSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return BrowserStateSanitizeResult{}, fmt.Errorf("read browser state: %w", readErr)
	}
	if closeErr != nil {
		return BrowserStateSanitizeResult{}, fmt.Errorf("close browser state: %w", closeErr)
	}
	sanitized, result, err := SanitizeBrowserStorageStateData(data)
	if err != nil {
		return BrowserStateSanitizeResult{}, err
	}
	if err := replacePrivateFile(path, sanitized); err != nil {
		return BrowserStateSanitizeResult{}, err
	}
	return result, nil
}

// SanitizeBrowserStorageStateData narrows an in-memory Playwright storage
// state to HH-owned domains. The result is the canonical JSON that callers
// persist.
func SanitizeBrowserStorageStateData(data []byte) ([]byte, BrowserStateSanitizeResult, error) {
	if len(data) > maxBrowserStateSize {
		return nil, BrowserStateSanitizeResult{}, errors.New("browser state exceeds size limit")
	}
	var state rawBrowserStorageState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, BrowserStateSanitizeResult{}, fmt.Errorf("decode browser state: %w", err)
	}
	result := BrowserStateSanitizeResult{CookiesBefore: len(state.Cookies), OriginsBefore: len(state.Origins)}
	state.Cookies = filterBrowserStateEntries(state.Cookies, func(raw json.RawMessage) bool {
		var metadata struct {
			Name   string `json:"name"`
			Domain string `json:"domain"`
		}
		return json.Unmarshal(raw, &metadata) == nil && strings.TrimSpace(metadata.Name) != "" && hhOwnedHost(metadata.Domain)
	})
	state.Origins = filterBrowserStateEntries(state.Origins, func(raw json.RawMessage) bool {
		var metadata struct {
			Origin string `json:"origin"`
		}
		if json.Unmarshal(raw, &metadata) != nil {
			return false
		}
		parsed, err := url.Parse(metadata.Origin)
		return err == nil && hhOwnedHost(parsed.Hostname())
	})
	result.CookiesAfter = len(state.Cookies)
	result.OriginsAfter = len(state.Origins)
	if result.CookiesAfter == 0 {
		return nil, BrowserStateSanitizeResult{}, errors.New("browser state contains no HH cookies")
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, BrowserStateSanitizeResult{}, fmt.Errorf("encode browser state: %w", err)
	}
	return encoded, result, nil
}

func filterBrowserStateEntries(entries []json.RawMessage, keep func(json.RawMessage) bool) []json.RawMessage {
	result := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		if keep(entry) {
			result = append(result, entry)
		}
	}
	return result
}

func hhOwnedHost(value string) bool {
	host := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
	return host == "hh.ru" || strings.HasSuffix(host, ".hh.ru") || host == "hhcdn.ru" || strings.HasSuffix(host, ".hhcdn.ru")
}

func replacePrivateFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".browser-state-*")
	if err != nil {
		return fmt.Errorf("create temporary browser state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary browser state: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary browser state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary browser state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary browser state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace browser state: %w", err)
	}
	return nil
}
