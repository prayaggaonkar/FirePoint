package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTPMFailureRefundsRPM(t *testing.T) {
	now := time.Unix(0, 0)

	t.Run("estimate larger than the bucket", func(t *testing.T) {
		l := NewLimiter(2, 100)
		ok, retry := l.Allow("t", 1000, now)
		if ok || retry != 0 {
			t.Fatalf("ok=%v retry=%s, want a permanent reject", ok, retry)
		}
		if ok, _ := l.Allow("t", 50, now); !ok {
			t.Fatal("rejected call still consumed a request token")
		}
		if ok, _ := l.Allow("t", 50, now); !ok {
			t.Fatal("second request slot was lost")
		}
		if ok, _ := l.Allow("t", 1, now); ok {
			t.Fatal("rpm burst exceeded 2")
		}
	})

	t.Run("not enough tokens yet", func(t *testing.T) {
		l := NewLimiter(2, 100)
		if ok, _ := l.Allow("t", 80, now); !ok {
			t.Fatal("first request")
		}
		ok, retry := l.Allow("t", 40, now)
		if ok || retry == 0 {
			t.Fatalf("ok=%v retry=%s, want a wait", ok, retry)
		}
		// The failed token take must give the request token back, or this
		// third call has nothing left to spend.
		if ok, _ := l.Allow("t", 20, now); !ok {
			t.Fatal("request token was not returned after the token bucket refused")
		}
	})
}

func TestLimiterRetryAfterMatchesRefill(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(60, 6000)
	for i := 0; i < 60; i++ {
		if ok, _ := l.Allow("t", 1, now); !ok {
			t.Fatalf("request %d", i)
		}
	}
	ok, retry := l.Allow("t", 1, now)
	if ok || retry != time.Second {
		t.Fatalf("ok=%v retry=%s, want 1s", ok, retry)
	}
	if ok, _ := l.Allow("t", 1, now.Add(time.Second-time.Nanosecond)); ok {
		t.Fatal("succeeded 1ns early")
	}
	if ok, _ := l.Allow("t", 1, now.Add(time.Second)); !ok {
		t.Fatal("failed after one second of refill")
	}
}

func TestAllowTenantKeepsBalanceWhenRatesRepeat(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(1, 1)
	if ok, _ := l.AllowTenant("alice", 1, 100, 1, now); !ok {
		t.Fatal("first request")
	}
	// The same rates on the next call must not refill the burst.
	if ok, _ := l.AllowTenant("alice", 1, 100, 1, now); ok {
		t.Fatal("repeating the tenant rates refilled the bucket")
	}
	if ok, _ := l.AllowTenant("bob", 2, 100, 1, now); !ok {
		t.Fatal("bob should have his own burst")
	}
	if ok, _ := l.AllowTenant("bob", 2, 100, 1, now); !ok {
		t.Fatal("bob's rpm is 2")
	}
	if ok, _ := l.AllowTenant("bob", 2, 100, 1, now); ok {
		t.Fatal("bob exceeded rpm 2")
	}
}

func TestTenantsHaveSeparateBuckets(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(1, 100)
	if ok, _ := l.Allow("a", 1, now); !ok {
		t.Fatal("tenant a")
	}
	if ok, _ := l.Allow("b", 1, now); !ok {
		t.Fatal("tenant b shared a bucket with a")
	}
	if ok, _ := l.Allow("a", 1, now); ok {
		t.Fatal("tenant a burst was larger than 1")
	}
}

func TestRefundReturnsUnusedTokens(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(10, 100)
	if ok, _ := l.Allow("t", 80, now); !ok {
		t.Fatal("reserve")
	}
	if ok, _ := l.Allow("t", 40, now); ok {
		t.Fatal("second reserve should not fit")
	}
	l.Refund("t", 60)
	if ok, _ := l.Allow("t", 40, now); !ok {
		t.Fatal("refunded tokens were not usable")
	}
	l.Refund("missing", 10)
}

func TestEvictIdleTenants(t *testing.T) {
	start := time.Unix(0, 0)
	l := NewLimiter(1, 100)
	if ok, _ := l.Allow("old", 1, start); !ok {
		t.Fatal("old")
	}
	seen := start.Add(10 * time.Minute)
	if ok, _ := l.Allow("fresh", 1, seen); !ok {
		t.Fatal("fresh")
	}

	l.EvictIdle(seen)
	if got := tenantCount(l); got != 2 {
		t.Fatalf("after exactly 10 minutes, %d tenants, want 2", got)
	}

	l.EvictIdle(seen.Add(time.Nanosecond))
	if got := tenantCount(l); got != 1 {
		t.Fatalf("after just over 10 minutes, %d tenants, want 1", got)
	}
	if _, ok := findTenant(l, "fresh"); !ok {
		t.Fatal("fresh tenant was evicted")
	}
	if _, ok := findTenant(l, "old"); ok {
		t.Fatal("idle tenant was kept")
	}
}

func TestConcurrentAllowOneTenant(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(50, 1_000_000)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _ := l.Allow("one", 10, now)
			if ok {
				allowed.Add(1)
				l.Refund("one", 10)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			l.EvictIdle(now)
		}
	}()
	wg.Wait()
	if got := allowed.Load(); got != 50 {
		t.Fatalf("allowed %d, want 50", got)
	}
}

func tenantCount(l *Limiter) int {
	n := 0
	for i := range l.shards {
		l.shards[i].mu.Lock()
		n += len(l.shards[i].tenants)
		l.shards[i].mu.Unlock()
	}
	return n
}

func findTenant(l *Limiter, id string) (*tenant, bool) {
	sh := l.shard(id)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	t, ok := sh.tenants[id]
	return t, ok
}
