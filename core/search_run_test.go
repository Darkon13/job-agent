package core

import "testing"

func TestSearchPageIdempotencyKeyIncludesGeneration(t *testing.T) {
	first, err := SearchPageIdempotencyKey("golang", 1, "")
	if err != nil {
		t.Fatalf("first key: %v", err)
	}
	second, err := SearchPageIdempotencyKey("golang", 2, "")
	if err != nil {
		t.Fatalf("second key: %v", err)
	}
	if first == second {
		t.Fatalf("generation change must produce a new key: %q", first)
	}
	again, err := SearchPageIdempotencyKey("golang", 1, "")
	if err != nil || again != first {
		t.Fatalf("same generation must be stable: %q vs %q err=%v", again, first, err)
	}
	if _, err := SearchPageIdempotencyKey("", 1, ""); err == nil {
		t.Fatal("empty search id must fail")
	}
}
