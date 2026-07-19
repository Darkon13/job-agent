package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
)

type ResumeToucherRegistry struct {
	mu       sync.RWMutex
	touchers map[core.ProfileID]adapter.ResumeToucher
}

func NewResumeToucherRegistry() *ResumeToucherRegistry {
	return &ResumeToucherRegistry{touchers: make(map[core.ProfileID]adapter.ResumeToucher)}
}

func (registry *ResumeToucherRegistry) Register(profileID core.ProfileID, toucher adapter.ResumeToucher) error {
	if profileID == "" || toucher == nil {
		return errors.New("resume toucher registration requires profile and toucher")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.touchers[profileID]; exists {
		return fmt.Errorf("resume toucher for profile %s is already registered", profileID)
	}
	registry.touchers[profileID] = toucher
	return nil
}

func (registry *ResumeToucherRegistry) Resolve(profileID core.ProfileID) (adapter.ResumeToucher, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	toucher, exists := registry.touchers[profileID]
	if !exists {
		return nil, &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "resumes.route", Message: "no resume toucher for profile"}
	}
	return toucher, nil
}

func (registry *ResumeToucherRegistry) Has(profileID core.ProfileID) bool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	_, exists := registry.touchers[profileID]
	return exists
}

func (registry *ResumeToucherRegistry) Count() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.touchers)
}

type ResumeTouchHandler struct{ touchers *ResumeToucherRegistry }

func NewResumeTouchHandler(touchers *ResumeToucherRegistry) (*ResumeTouchHandler, error) {
	if touchers == nil {
		return nil, errors.New("resume touch handler requires toucher registry")
	}
	return &ResumeTouchHandler{touchers: touchers}, nil
}

func (handler *ResumeTouchHandler) Handle(ctx context.Context, task core.Task) error {
	var payload core.ResumeTouchPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode resume touch task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ProfileID != task.ProfileID {
		return errors.New("resume touch task profile mismatch")
	}
	toucher, err := handler.touchers.Resolve(payload.ProfileID)
	if err != nil {
		return err
	}
	_, err = toucher.TouchResume(ctx, adapter.ResumeTouchCommand{
		ProfileID: payload.ProfileID, ResumeID: payload.ResumeID, IdempotencyKey: task.IdempotencyKey,
	})
	return err
}
