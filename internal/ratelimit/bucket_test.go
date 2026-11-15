package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBucketRefillBurstAndOverCapacity(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name      string
		capacity  float64
		rate      float64
		take      float64
		wait      time.Duration
		takeAgain float64
		wantOK    bool
		wantRetry time.Duration
	}{
		{
			name:     "one second restores the per-second rate",
			capacity: 10, rate: 5, take: 10,
			wait: time.Second, takeAgain: 5, wantOK: true,
		},
		{
			name:     "half a second restores half the rate",
			capacity: 10, rate: 10, take: 10,
			wait: 500 * time.Millisecond, takeAgain: 5, wantOK: true,
		},
		{
			name:     "half a second is short of six",
			capacity: 10, rate: 10, take: 10,
			wait: 500 * time.Millisecond, takeAgain: 6,
			wantOK: false, wantRetry: 100 * time.Millisecond,
		},
		{
			name:     "a long wait does not pass capacity",
			capacity: 10, rate: 10, take: 10,
			wait: time.Hour, takeAgain: 10, wantOK: true,
		},
		{
			name:     "more than capacity can never succeed",
			capacity: 10, rate: 10, take: 10,
			wait: time.Hour, takeAgain: 11,
			wantOK: false, wantRetry: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBucket(tc.capacity, tc.rate, start)
			if ok, _ := b.TryTake(tc.take, start); !ok {
				t.Fatal("initial take failed")
			}
			ok, retry := b.TryTake(tc.takeAgain, start.Add(tc.wait))
			if ok != tc.wantOK || retry != tc.wantRetry {
				t.Fatalf("ok=%v retry=%s, want ok=%v retry=%s", ok, retry, tc.wantOK, tc.wantRetry)
			}
			if tc.takeAgain > tc.capacity {
				if ok, _ := b.TryTake(tc.capacity, start.Add(tc.wait)); !ok {
					t.Fatal("over-capacity take changed the bucket")
				}
			}
		})
	}
}

func TestRetryAfterIsLongEnoughAndNoLonger(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	tests := []struct {
		rate float64
		n    float64
		want time.Duration
	}{
		{rate: 2, n: 1, want: 500 * time.Millisecond},
		{rate: 4, n: 1, want: 250 * time.Millisecond},
		{rate: 2, n: 3, want: 1500 * time.Millisecond},
		{rate: 3, n: 1, want: 333333334 * time.Nanosecond},
	}

	for _, tc := range tests {
		b := emptyBucket(t, 10, tc.rate, start)
		ok, got := b.TryTake(tc.n, start)
		if ok || got != tc.want {
			t.Fatalf("rate %v n %v: ok=%v retry=%s, want %s", tc.rate, tc.n, ok, got, tc.want)
		}

		early := emptyBucket(t, 10, tc.rate, start)
		if ok, _ := early.TryTake(tc.n, start.Add(got-time.Nanosecond)); ok {
			t.Fatalf("rate %v n %v succeeded 1ns early", tc.rate, tc.n)
		}
		onTime := emptyBucket(t, 10, tc.rate, start)
		if ok, _ := onTime.TryTake(tc.n, start.Add(got)); !ok {
			t.Fatalf("rate %v n %v failed after retryAfter", tc.rate, tc.n)
		}
	}
}

func TestRefundStopsAtCapacity(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	b := NewBucket(10, 1, start)
	if ok, _ := b.TryTake(10, start); !ok {
		t.Fatal("drain failed")
	}
	b.Refund(0)
	b.Refund(-3)
	if ok, _ := b.TryTake(1, start); ok {
		t.Fatal("non-positive refund added tokens")
	}
	b.Refund(4)
	if ok, _ := b.TryTake(4, start); !ok {
		t.Fatal("refunded tokens were not available")
	}
	b.Refund(100)
	if ok, _ := b.TryTake(10, start); !ok {
		t.Fatal("refund should fill to capacity")
	}
	if ok, _ := b.TryTake(1, start); ok {
		t.Fatal("refund passed capacity")
	}
}

func TestBucketConcurrentTryTake(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBucket(50, 0, now)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := b.TryTake(1, now); ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 50 {
		t.Fatalf("allowed %d, want 50", got)
	}
}

func BenchmarkTryTake(b *testing.B) {
	now := time.Unix(0, 0)
	// Full and not refilling: each call only locks, checks, and subtracts.
	bucket := NewBucket(1e18, 0, now)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, _ := bucket.TryTake(1, now)
		if !ok {
			b.Fatal("bucket rejected a token")
		}
	}
}

func emptyBucket(t *testing.T, capacity, rate float64, now time.Time) *Bucket {
	t.Helper()
	b := NewBucket(capacity, rate, now)
	if ok, _ := b.TryTake(capacity, now); !ok {
		t.Fatal("drain failed")
	}
	return b
}
