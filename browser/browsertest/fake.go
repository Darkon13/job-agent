// Package browsertest provides an in-memory browser.Client for contract tests.
package browsertest

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/Darkon13/job-agent/browser"
	"github.com/Darkon13/job-agent/core"
)

type Call struct {
	Method    string
	ProfileID core.ProfileID
	Request   any
}

// Fake records calls and returns scripted results. Errors keyed by method name
// override the configured result.
type Fake struct {
	mu sync.Mutex

	Calls []Call

	HealthResult   browser.Health
	ReadyResult    browser.Ready
	EnsureResult   browser.ContextInfo
	ListResult     []browser.ContextInfo
	GotoResult     browser.GotoResult
	PageResult     browser.PageInfo
	ContentResult  browser.ContentResult
	ScreenshotData []byte
	StorageState   json.RawMessage
	Errors         map[string]error

	// PageFunc and LocatorFunc allow callers to script per-call behavior
	// (for example URL transitions during a login).
	PageFunc    func(core.ProfileID) (browser.PageInfo, error)
	LocatorFunc func(core.ProfileID, browser.LocatorRequest) error
}

var _ browser.Client = (*Fake)(nil)

func New() *Fake {
	return &Fake{Errors: make(map[string]error)}
}

func (fake *Fake) record(method string, profileID core.ProfileID, request any) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.Calls = append(fake.Calls, Call{Method: method, ProfileID: profileID, Request: request})
	return fake.Errors[method]
}

func (fake *Fake) CallsOf(method string) []Call {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	result := make([]Call, 0)
	for _, call := range fake.Calls {
		if call.Method == method {
			result = append(result, call)
		}
	}
	return result
}

func (fake *Fake) Health(context.Context) (browser.Health, error) {
	if err := fake.record("health", "", nil); err != nil {
		return browser.Health{}, err
	}
	return fake.HealthResult, nil
}

func (fake *Fake) Ready(context.Context) (browser.Ready, error) {
	if err := fake.record("ready", "", nil); err != nil {
		return browser.Ready{}, err
	}
	return fake.ReadyResult, nil
}

func (fake *Fake) Ensure(_ context.Context, profileID core.ProfileID, request browser.EnsureRequest) (browser.ContextInfo, error) {
	if err := fake.record("ensure", profileID, request); err != nil {
		return browser.ContextInfo{}, err
	}
	info := fake.EnsureResult
	info.ProfileID = profileID
	return info, nil
}

func (fake *Fake) List(context.Context) ([]browser.ContextInfo, error) {
	if err := fake.record("list", "", nil); err != nil {
		return nil, err
	}
	return fake.ListResult, nil
}

func (fake *Fake) Close(_ context.Context, profileID core.ProfileID, purge bool) error {
	return fake.record("close", profileID, purge)
}

func (fake *Fake) ExportStorageState(_ context.Context, profileID core.ProfileID) (json.RawMessage, error) {
	if err := fake.record("export_state", profileID, nil); err != nil {
		return nil, err
	}
	return fake.StorageState, nil
}

func (fake *Fake) ImportStorageState(_ context.Context, profileID core.ProfileID, state json.RawMessage) error {
	return fake.record("import_state", profileID, state)
}

func (fake *Fake) Goto(_ context.Context, profileID core.ProfileID, request browser.GotoRequest) (browser.GotoResult, error) {
	if err := fake.record("goto", profileID, request); err != nil {
		return browser.GotoResult{}, err
	}
	return fake.GotoResult, nil
}

func (fake *Fake) Page(_ context.Context, profileID core.ProfileID) (browser.PageInfo, error) {
	if err := fake.record("page", profileID, nil); err != nil {
		return browser.PageInfo{}, err
	}
	if fake.PageFunc != nil {
		return fake.PageFunc(profileID)
	}
	return fake.PageResult, nil
}

func (fake *Fake) Content(_ context.Context, profileID core.ProfileID, request browser.ContentRequest) (browser.ContentResult, error) {
	if err := fake.record("content", profileID, request); err != nil {
		return browser.ContentResult{}, err
	}
	return fake.ContentResult, nil
}

func (fake *Fake) Screenshot(_ context.Context, profileID core.ProfileID, request browser.ScreenshotRequest) ([]byte, error) {
	if err := fake.record("screenshot", profileID, request); err != nil {
		return nil, err
	}
	return append([]byte(nil), fake.ScreenshotData...), nil
}

func (fake *Fake) Locator(_ context.Context, profileID core.ProfileID, request browser.LocatorRequest) error {
	if err := fake.record("locator", profileID, request); err != nil {
		return err
	}
	if fake.LocatorFunc != nil {
		return fake.LocatorFunc(profileID, request)
	}
	return nil
}
