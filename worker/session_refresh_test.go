package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Darkon13/job-agent/core"
)

type fakeSessionRefresher struct {
	state json.RawMessage
	err   error
	calls int
}

func (refresher *fakeSessionRefresher) RefreshStorageState(context.Context, core.ProfileID) (json.RawMessage, error) {
	refresher.calls++
	if refresher.err != nil {
		return nil, refresher.err
	}
	return refresher.state, nil
}

func sessionRefreshTask(t *testing.T, profileID core.ProfileID) core.Task {
	t.Helper()
	payload, err := json.Marshal(core.ProfileSessionRefreshPayload{ProfileID: profileID})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return core.Task{ID: "task-1", Type: core.TaskProfileSessionRefresh, Payload: payload}
}

func TestSessionRefreshHandlerWritesSanitizedState(t *testing.T) {
	directory := t.TempDir()
	stateFile := filepath.Join(directory, "profile.json")
	refresher := &fakeSessionRefresher{state: json.RawMessage(`{"cookies":[{"name":"a"}],"origins":[]}`)}
	registry := NewSessionRefresherRegistry()
	if err := registry.Register("primary", refresher, stateFile); err != nil {
		t.Fatalf("register: %v", err)
	}
	sanitizedInput := ""
	handler, err := NewSessionRefreshHandler(registry, func(data []byte) ([]byte, error) {
		sanitizedInput = string(data)
		return []byte(`{"cookies":[{"name":"a","domain":".hh.ru"}],"origins":[]}`), nil
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if err := handler.Handle(context.Background(), sessionRefreshTask(t, "primary")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if refresher.calls != 1 || sanitizedInput == "" {
		t.Fatalf("calls=%d sanitizedInput=%q", refresher.calls, sanitizedInput)
	}
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if string(data) != `{"cookies":[{"name":"a","domain":".hh.ru"}],"origins":[]}` {
		t.Fatalf("state file=%s", data)
	}
	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func TestSessionRefreshHandlerFailsForUnknownProfile(t *testing.T) {
	registry := NewSessionRefresherRegistry()
	handler, err := NewSessionRefreshHandler(registry, func(data []byte) ([]byte, error) { return data, nil })
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if err := handler.Handle(context.Background(), sessionRefreshTask(t, "missing")); err == nil {
		t.Fatal("expected unknown profile to fail")
	}
}

func TestSessionRefreshHandlerMapsSanitizeFailureToUnauthorized(t *testing.T) {
	directory := t.TempDir()
	registry := NewSessionRefresherRegistry()
	if err := registry.Register("primary", &fakeSessionRefresher{state: json.RawMessage(`{}`)}, filepath.Join(directory, "profile.json")); err != nil {
		t.Fatalf("register: %v", err)
	}
	handler, err := NewSessionRefreshHandler(registry, func([]byte) ([]byte, error) {
		return nil, errors.New("no platform cookies")
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	err = handler.Handle(context.Background(), sessionRefreshTask(t, "primary"))
	if err == nil || !core.ErrorIsCategory(err, core.ErrorUnauthorized) {
		t.Fatalf("error=%v", err)
	}
}
