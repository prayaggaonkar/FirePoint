package breaker

import (
	"errors"
	"testing"
	"time"
)

func TestRegistryKeepsABreakerPerModel(t *testing.T) {
	r := NewRegistry(DefaultConfig())
	now := time.Unix(1_700_000_000, 0)
	if r.Get("big") != r.Get("big") {
		t.Fatal("same model returned two breakers")
	}
	if r.Get("big") == r.Get("small") {
		t.Fatal("two models shared a breaker")
	}
	for i := 0; i < 20; i++ {
		done, err := r.Allow("big", now)
		if err != nil {
			t.Fatal(err)
		}
		done(Failed)
	}
	if _, err := r.Allow("big", now); !errors.Is(err, ErrOpen) {
		t.Fatalf("big: %v", err)
	}
	if _, err := r.Allow("small", now); err != nil {
		t.Fatalf("small was closed by big's failures: %v", err)
	}
}
