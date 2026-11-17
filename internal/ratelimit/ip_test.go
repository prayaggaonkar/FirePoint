package ratelimit

import (
	"testing"
	"time"
)

func TestIPLimiterBurstThenRefill(t *testing.T) {
	now := time.Unix(0, 0)
	lim := NewIPLimiter(2)
	if ok, _ := lim.Allow("1.2.3.4", now); !ok {
		t.Fatal("first request")
	}
	if ok, _ := lim.Allow("1.2.3.4", now); !ok {
		t.Fatal("second request")
	}
	if ok, retry := lim.Allow("1.2.3.4", now); ok || retry <= 0 {
		t.Fatalf("third request ok=%v retry=%s", ok, retry)
	}
	// A different address has its own burst.
	if ok, _ := lim.Allow("5.6.7.8", now); !ok {
		t.Fatal("other address")
	}
	later := now.Add(30 * time.Second)
	if ok, _ := lim.Allow("1.2.3.4", later); !ok {
		t.Fatal("after refill")
	}
}

func TestIPLimiterEmptyAddressSharesOneBucket(t *testing.T) {
	now := time.Unix(0, 0)
	lim := NewIPLimiter(1)
	if ok, _ := lim.Allow("", now); !ok {
		t.Fatal("first empty")
	}
	if ok, _ := lim.Allow("", now); ok {
		t.Fatal("second empty should share the bucket")
	}
}

func TestIPLimiterEvictsIdleAddresses(t *testing.T) {
	now := time.Unix(0, 0)
	lim := NewIPLimiter(1)
	if ok, _ := lim.Allow("old", now); !ok {
		t.Fatal("old")
	}
	later := now.Add(idleAfter + time.Minute)
	if ok, _ := lim.Allow("new", later); !ok {
		t.Fatal("new")
	}
	if got := lim.len(); got != 1 {
		t.Fatalf("buckets = %d, want 1 after idle eviction", got)
	}
}
