// Package auth owns the interactive login control plane: persistent sessions
// in storage and short-lived challenge payloads that never reach the database.
package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	DefaultChallengeTTL     = 10 * time.Minute
	MaximumChallengeTTL     = 30 * time.Minute
	MaximumChallengePayload = 2 << 20
)

var (
	// ErrChallengeNotFound reports an unknown, consumed or expired payload.
	ErrChallengeNotFound = errors.New("auth challenge payload not found")
	// ErrChallengeInvalid reports an empty or oversized payload.
	ErrChallengeInvalid = errors.New("auth challenge payload is invalid")
)

type ChallengePayload struct {
	MediaType string
	Data      []byte
}

type ChallengeStore interface {
	Put(ctx context.Context, id string, payload ChallengePayload, ttl time.Duration, now time.Time) error
	Get(ctx context.Context, id string, now time.Time) (ChallengePayload, error)
	Delete(ctx context.Context, id string) error
}

type memoryChallengeEntry struct {
	payload   ChallengePayload
	expiresAt time.Time
}

// MemoryChallengeStore keeps payloads for the lifetime of one backend process.
// Expired entries are dropped lazily on access.
type MemoryChallengeStore struct {
	mu      sync.Mutex
	entries map[string]memoryChallengeEntry
}

func NewMemoryChallengeStore() *MemoryChallengeStore {
	return &MemoryChallengeStore{entries: make(map[string]memoryChallengeEntry)}
}

func (store *MemoryChallengeStore) Put(ctx context.Context, id string, payload ChallengePayload, ttl time.Duration, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil {
		return errors.New("auth challenge store is nil")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrChallengeInvalid
	}
	if len(payload.MediaType) > 64 || len(payload.Data) == 0 || len(payload.Data) > MaximumChallengePayload {
		return ErrChallengeInvalid
	}
	if ttl <= 0 || ttl > MaximumChallengeTTL || now.IsZero() {
		return ErrChallengeInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.entries == nil {
		store.entries = make(map[string]memoryChallengeEntry)
	}
	store.entries[id] = memoryChallengeEntry{
		payload:   ChallengePayload{MediaType: payload.MediaType, Data: append([]byte(nil), payload.Data...)},
		expiresAt: now.Add(ttl),
	}
	return nil
}

func (store *MemoryChallengeStore) Get(ctx context.Context, id string, now time.Time) (ChallengePayload, error) {
	if err := ctx.Err(); err != nil {
		return ChallengePayload{}, err
	}
	if store == nil {
		return ChallengePayload{}, ErrChallengeNotFound
	}
	id = strings.TrimSpace(id)
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, exists := store.entries[id]
	if !exists {
		return ChallengePayload{}, ErrChallengeNotFound
	}
	if !now.Before(entry.expiresAt) {
		delete(store.entries, id)
		return ChallengePayload{}, ErrChallengeNotFound
	}
	return ChallengePayload{MediaType: entry.payload.MediaType, Data: append([]byte(nil), entry.payload.Data...)}, nil
}

func (store *MemoryChallengeStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.entries, strings.TrimSpace(id))
	return nil
}
