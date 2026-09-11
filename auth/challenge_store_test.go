package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryChallengeStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store := NewMemoryChallengeStore()
	payload := ChallengePayload{MediaType: "image/png", Data: []byte("png-bytes")}
	if err := store.Put(ctx, "challenge-1", payload, DefaultChallengeTTL, now); err != nil {
		t.Fatalf("put: %v", err)
	}
	payload.Data[0] = 'x'
	loaded, err := store.Get(ctx, "challenge-1", now.Add(time.Minute))
	if err != nil || loaded.MediaType != "image/png" || string(loaded.Data) != "png-bytes" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	loaded.Data[0] = 'y'
	reloaded, err := store.Get(ctx, "challenge-1", now.Add(time.Minute))
	if err != nil || string(reloaded.Data) != "png-bytes" {
		t.Fatalf("store payload was aliased: %#v err=%v", reloaded, err)
	}
	if err := store.Delete(ctx, "challenge-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, "challenge-1", now); !errors.Is(err, ErrChallengeNotFound) {
		t.Fatalf("get deleted error = %v", err)
	}
}

func TestMemoryChallengeStoreExpiresAndValidates(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	store := NewMemoryChallengeStore()
	payload := ChallengePayload{MediaType: "text/plain", Data: []byte("https://example.test/captcha")}
	if err := store.Put(ctx, "challenge-1", payload, time.Minute, now); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := store.Get(ctx, "challenge-1", now.Add(time.Minute)); !errors.Is(err, ErrChallengeNotFound) {
		t.Fatalf("expired payload error = %v", err)
	}
	for _, invalid := range []struct {
		name    string
		id      string
		payload ChallengePayload
		ttl     time.Duration
	}{
		{name: "empty id", payload: payload, ttl: time.Minute},
		{name: "empty payload", id: "challenge-2", ttl: time.Minute},
		{name: "oversized payload", id: "challenge-2", payload: ChallengePayload{MediaType: "image/png", Data: make([]byte, MaximumChallengePayload+1)}, ttl: time.Minute},
		{name: "zero ttl", id: "challenge-2", payload: payload},
		{name: "too long ttl", id: "challenge-2", payload: payload, ttl: MaximumChallengeTTL + time.Second},
	} {
		if err := store.Put(ctx, invalid.id, invalid.payload, invalid.ttl, now); !errors.Is(err, ErrChallengeInvalid) {
			t.Fatalf("%s error = %v", invalid.name, err)
		}
	}
}
