package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Darkon13/job-agent/core"
)

// SessionRefresher exports the live storage state of one browser profile.
type SessionRefresher interface {
	RefreshStorageState(context.Context, core.ProfileID) (json.RawMessage, error)
}

// SessionRefresherRegistry binds each profile to its storage state file so a
// scheduled refresh can persist the exported cookies back to disk.
type SessionRefresherRegistry struct {
	mu         sync.RWMutex
	refreshers map[core.ProfileID]SessionRefresher
	stateFiles map[core.ProfileID]string
}

func NewSessionRefresherRegistry() *SessionRefresherRegistry {
	return &SessionRefresherRegistry{
		refreshers: make(map[core.ProfileID]SessionRefresher),
		stateFiles: make(map[core.ProfileID]string),
	}
}

func (registry *SessionRefresherRegistry) Register(profileID core.ProfileID, refresher SessionRefresher, stateFile string) error {
	if registry == nil {
		return errors.New("session refresher registry is nil")
	}
	if profileID == "" || refresher == nil {
		return errors.New("session refresher registration requires profile and refresher")
	}
	if stateFile = filepath.Clean(stateFile); stateFile == "." || stateFile == "" {
		return errors.New("session refresher registration requires a state file")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.refreshers[profileID]; exists {
		return fmt.Errorf("session refresher for profile %s is already registered", profileID)
	}
	registry.refreshers[profileID] = refresher
	registry.stateFiles[profileID] = stateFile
	return nil
}

func (registry *SessionRefresherRegistry) Resolve(profileID core.ProfileID) (SessionRefresher, string, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	refresher, exists := registry.refreshers[profileID]
	if !exists {
		return nil, "", &core.OperationError{
			Category: core.ErrorPermanentFailure, Operation: "profile.session_refresh",
			Message: "no session refresher for profile",
		}
	}
	return refresher, registry.stateFiles[profileID], nil
}

func (registry *SessionRefresherRegistry) Has(profileID core.ProfileID) bool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	_, exists := registry.refreshers[profileID]
	return exists
}

func (registry *SessionRefresherRegistry) Count() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.refreshers)
}

// SessionStateSanitizer narrows a raw Playwright export to platform cookies.
type SessionStateSanitizer func([]byte) ([]byte, error)

type SessionRefreshHandler struct {
	refreshers *SessionRefresherRegistry
	sanitize   SessionStateSanitizer
}

func NewSessionRefreshHandler(refreshers *SessionRefresherRegistry, sanitize SessionStateSanitizer) (*SessionRefreshHandler, error) {
	if refreshers == nil || sanitize == nil {
		return nil, errors.New("session refresh handler requires refreshers and a sanitizer")
	}
	return &SessionRefreshHandler{refreshers: refreshers, sanitize: sanitize}, nil
}

func (handler *SessionRefreshHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ProfileSessionRefreshPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode profile session refresh task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	refresher, stateFile, err := handler.refreshers.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	raw, err := refresher.RefreshStorageState(ctx, payload.ProfileID)
	if err != nil {
		return err
	}
	sanitized, err := handler.sanitize(raw)
	if err != nil {
		return &core.OperationError{
			Category: core.ErrorUnauthorized, Operation: "profile.session_refresh",
			Message: "browser session does not contain platform cookies", Cause: err,
		}
	}
	if err := writePrivateStateFile(stateFile, sanitized); err != nil {
		return &core.OperationError{
			Category: core.ErrorTemporaryFailure, Operation: "profile.session_refresh",
			Message: "write browser storage state", Cause: err,
		}
	}
	return nil
}

// writePrivateStateFile replaces the state file atomically with owner-only
// permissions so a concurrent reader never observes a partial write.
func writePrivateStateFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".session-refresh-*")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("restrict temporary state file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temporary state file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close temporary state file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace browser state file: %w", err)
	}
	return nil
}
