package breaker

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRegistryKeepsABreakerPerModel(t *testing.T) {
	r := NewRegistry(DefaultConfig())
	now := time.Unix(1_700_000_000, 0)
	big := r.Get("big")
	if big != r.Get("big") {
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

func TestRegistryIsBounded(t *testing.T) {
	r := NewRegistry(DefaultConfig())
	for i := 0; i < MaxModels+100; i++ {
		r.Get(fmt.Sprintf("m%d", i))
	}
	r.mu.Lock()
	n := len(r.m)
	r.mu.Unlock()
	if n != MaxModels {
		t.Fatalf("registry holds %d breakers, want %d", n, MaxModels)
	}
	if r.Get("brand-new-1") != r.Get("brand-new-2") {
		t.Fatal("names past the cap should share the overflow breaker")
	}
	if r.Get("m0") == r.Get("brand-new-1") {
		t.Fatal("an existing model must keep its own breaker")
	}
}
