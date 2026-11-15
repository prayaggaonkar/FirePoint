package breaker

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClosedOpensAtThreshold(t *testing.T) {
	b := New(DefaultConfig())
	now := time.Unix(1_700_000_000, 0)

	for i := 0; i < 11; i++ {
		mustAllow(t, b, now)(Succeeded)
	}
	for i := 0; i < 9; i++ {
		mustAllow(t, b, now)(Failed)
	}
	if b.State() != Closed {
		t.Fatalf("state = %s after 20 calls under the threshold", b.State())
	}

	b = New(DefaultConfig())
	for i := 0; i < 10; i++ {
		mustAllow(t, b, now)(Succeeded)
	}
	for i := 0; i < 9; i++ {
		mustAllow(t, b, now)(Failed)
	}
	if b.State() != Closed {
		t.Fatalf("state = %s before the minimum sample", b.State())
	}
	mustAllow(t, b, now)(Failed)
	if b.State() != Open {
		t.Fatalf("state = %s, want open at 10/20 failures", b.State())
	}
}

func TestIgnoredAndBackoffAreNotSamples(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinRequests = 2
	b := New(cfg)
	now := time.Unix(1_700_000_000, 0)
	mustAllow(t, b, now)(Ignored)
	mustAllow(t, b, now)(Backoff)
	mustAllow(t, b, now)(Failed)
	if b.State() != Closed {
		t.Fatalf("state = %s, want closed", b.State())
	}
}

func TestOpenStaysOpenUntilCooldown(t *testing.T) {
	b, opened := openBreaker(t)
	if _, err := b.Allow(opened); !errors.Is(err, ErrOpen) {
		t.Fatalf("at open instant: %v", err)
	}
	if _, err := b.Allow(opened.Add(15*time.Second - time.Nanosecond)); !errors.Is(err, ErrOpen) {
		t.Fatalf("1ns early: %v", err)
	}
	if b.State() != Open {
		t.Fatalf("state = %s, want open", b.State())
	}
}

func TestOpenBecomesHalfOpenAfterCooldown(t *testing.T) {
	b, opened := openBreaker(t)
	done, err := b.Allow(opened.Add(15 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if b.State() != HalfOpen {
		t.Fatalf("state = %s, want half-open", b.State())
	}
	if done == nil {
		t.Fatal("half-open probe was not allowed")
	}
}

func TestHalfOpenClosesAfterProbesSucceed(t *testing.T) {
	b, now := halfOpenBreaker(t)
	for i := 0; i < 3; i++ {
		mustAllow(t, b, now)(Succeeded)
		if i < 2 && b.State() != HalfOpen {
			t.Fatalf("closed after %d probes", i+1)
		}
	}
	if b.State() != Closed {
		t.Fatalf("state = %s, want closed", b.State())
	}
	// The failure window was cleared, so one new error does not reopen it.
	mustAllow(t, b, now)(Failed)
	if b.State() != Closed {
		t.Fatalf("state = %s after one fresh failure", b.State())
	}
}

func TestHalfOpenOpensOnProbeFailure(t *testing.T) {
	b, now := halfOpenBreaker(t)
	first := mustAllow(t, b, now)
	second := mustAllow(t, b, now)
	first(Failed)
	if b.State() != Open {
		t.Fatalf("state = %s, want open", b.State())
	}
	second(Succeeded)
	if b.State() != Open {
		t.Fatalf("a late probe success left state %s", b.State())
	}
	if _, err := b.Allow(now); !errors.Is(err, ErrOpen) {
		t.Fatalf("after failed probe: %v", err)
	}
	if _, err := b.Allow(now.Add(15*time.Second - time.Nanosecond)); !errors.Is(err, ErrOpen) {
		t.Fatal("cooldown did not restart from the failed probe")
	}
	if _, err := b.Allow(now.Add(15 * time.Second)); err != nil {
		t.Fatalf("after a new cooldown: %v", err)
	}
}

func TestHalfOpenProbeLimit(t *testing.T) {
	b, now := halfOpenBreaker(t)
	var release []func(Outcome)
	for i := 0; i < 3; i++ {
		release = append(release, mustAllow(t, b, now))
	}
	if _, err := b.Allow(now); !errors.Is(err, ErrOpen) {
		t.Fatal("a fourth concurrent probe was allowed")
	}
	if b.State() != HalfOpen {
		t.Fatalf("state = %s", b.State())
	}
	release[0](Succeeded)
	if _, err := b.Allow(now); err != nil {
		t.Fatalf("probe slot was not released: %v", err)
	}
}

func TestConcurrentProbeLimit(t *testing.T) {
	b, now := halfOpenBreaker(t)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.Allow(now); err == nil {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 3 {
		t.Fatalf("allowed %d probes, want 3", got)
	}
}

func TestConcurrentClosedRecords(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinRequests = 100000
	b := New(cfg)
	now := time.Unix(1_700_000_000, 0)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			done, err := b.Allow(now)
			if err != nil {
				t.Errorf("allow: %v", err)
				return
			}
			if i%2 == 0 {
				done(Succeeded)
			} else {
				done(Failed)
			}
		}(i)
	}
	wg.Wait()
	if b.State() != Closed {
		t.Fatalf("state = %s", b.State())
	}
}

func BenchmarkAllowRecord(b *testing.B) {
	br := New(DefaultConfig())
	now := time.Unix(1_700_000_000, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		done, err := br.Allow(now)
		if err != nil {
			b.Fatal(err)
		}
		done(Succeeded)
	}
}

func openBreaker(t *testing.T) (*Breaker, time.Time) {
	t.Helper()
	b := New(DefaultConfig())
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 20; i++ {
		mustAllow(t, b, now)(Failed)
	}
	if b.State() != Open {
		t.Fatalf("state = %s, want open", b.State())
	}
	return b, now
}

func halfOpenBreaker(t *testing.T) (*Breaker, time.Time) {
	t.Helper()
	b, opened := openBreaker(t)
	now := opened.Add(15 * time.Second)
	// Allow is what leaves Open, and that call also takes a probe slot. Give the
	// slot back without counting a success so tests start with a full budget.
	if _, err := b.Allow(now); err != nil {
		t.Fatal(err)
	}
	if b.State() != HalfOpen {
		t.Fatalf("state = %s, want half-open", b.State())
	}
	b.mu.Lock()
	b.inflight--
	b.mu.Unlock()
	return b, now
}

func mustAllow(t *testing.T, b *Breaker, now time.Time) func(Outcome) {
	t.Helper()
	done, err := b.Allow(now)
	if err != nil {
		t.Fatal(err)
	}
	if done == nil {
		t.Fatal("nil callback")
	}
	return done
}
