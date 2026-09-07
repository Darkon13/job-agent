package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/storage"
)

type ProfileStateWriterRegistry struct {
	mu      sync.RWMutex
	writers map[core.ProfileID]adapter.ProfileStateWriter
}

func NewProfileStateWriterRegistry() *ProfileStateWriterRegistry {
	return &ProfileStateWriterRegistry{writers: make(map[core.ProfileID]adapter.ProfileStateWriter)}
}

func (registry *ProfileStateWriterRegistry) Register(profileID core.ProfileID, writer adapter.ProfileStateWriter) error {
	if profileID == "" || writer == nil {
		return errors.New("profile state writer registration requires profile and writer")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.writers[profileID]; exists {
		return fmt.Errorf("profile state writer for profile %s is already registered", profileID)
	}
	registry.writers[profileID] = writer
	return nil
}

func (registry *ProfileStateWriterRegistry) Resolve(profileID core.ProfileID) (adapter.ProfileStateWriter, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	writer := registry.writers[profileID]
	if writer == nil {
		return nil, &core.OperationError{Category: core.ErrorPermanentFailure, Operation: "profile_state.route", Message: "no profile state writer for profile"}
	}
	return writer, nil
}

func (registry *ProfileStateWriterRegistry) Count() int {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return len(registry.writers)
}

type ProfileStateApplyHandler struct {
	proposals storage.ProfileStateProposalRepository
	writers   *ProfileStateWriterRegistry
}

func NewProfileStateApplyHandler(proposals storage.ProfileStateProposalRepository, writers *ProfileStateWriterRegistry) (*ProfileStateApplyHandler, error) {
	if proposals == nil || writers == nil {
		return nil, errors.New("profile state apply handler requires proposals and writer registry")
	}
	return &ProfileStateApplyHandler{proposals: proposals, writers: writers}, nil
}

func (handler *ProfileStateApplyHandler) Handle(ctx context.Context, task core.Task) error {
	if task.Type != core.TaskProfileStateApply {
		return errors.New("profile state apply handler received another task type")
	}
	var payload core.ProfileStateApplyPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode profile state apply task: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	proposal, err := handler.proposals.ProfileStateProposal(ctx, payload.ProposalID)
	if err != nil {
		return err
	}
	if proposal.ProfileID != task.ProfileID {
		return errors.New("profile state apply task profile mismatch")
	}
	if proposal.Status != core.ProfileStateProposalPlanned {
		return errors.New("profile state apply task requires a proposal with changes")
	}
	writer, err := handler.writers.Resolve(proposal.ProfileID)
	if err != nil {
		return err
	}
	result, err := writer.ApplyProfileState(ctx, proposal)
	if err != nil {
		return err
	}
	if err := result.Observation.Validate(); err != nil {
		return fmt.Errorf("profile state writer returned invalid observation: %w", err)
	}
	if result.Observation.ProfileID != proposal.ProfileID {
		return errors.New("profile state writer returned another profile")
	}
	pending, err := proposal.ChangesToApply(result.Observation)
	if err != nil {
		return err
	}
	if len(pending) != 0 {
		return &core.OperationError{Category: core.ErrorAmbiguousResult, Operation: "profile_state.apply", Message: "profile state writer returned an unverified result"}
	}
	return nil
}
