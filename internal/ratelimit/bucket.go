package ratelimit

import (
	"math"
	"sync"
	"time"
)

// Bucket is a token bucket. It refills only when a method is called, using the
// caller's timestamp, so tests can move time forward without sleeping and the
// limiter does not need a goroutine per tenant.
type Bucket struct {
	mu       sync.Mutex
	capacity float64
	rate     float64 // tokens added per second
	tokens   float64
	updated  time.Time
}

// NewBucket starts full. capacity is the most tokens it can hold. rate is how
// many tokens are added each second. A limit of N per minute uses capacity N
// and rate N/60.
func NewBucket(capacity, rate float64, now time.Time) *Bucket {
	if capacity < 0 {
		capacity = 0
	}
	if rate < 0 {
		rate = 0
	}
	return &Bucket{
		capacity: capacity,
		rate:     rate,
		tokens:   capacity,
		updated:  now,
	}
}

// TryTake removes n tokens when the bucket has them. n larger than capacity
// can never succeed, even after a full refill, so retryAfter is zero.
func (b *Bucket) TryTake(n float64, now time.Time) (ok bool, retryAfter time.Duration) {
	if n > b.capacity {
		return false, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refill(now)
	if n <= 0 {
		return true, 0
	}
	if b.tokens < n {
		return false, durationUntil(n-b.tokens, b.rate)
	}
	b.tokens -= n
	return true, 0
}

// SetRates changes the burst and the drip. The current balance is kept,
// capped at the new burst, so a config reload does not refill the bucket
// or throw away tokens the tenant still has.
func (b *Bucket) SetRates(capacity, rate float64) {
	if capacity < 0 {
		capacity = 0
	}
	if rate < 0 {
		rate = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.capacity = capacity
	b.rate = rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
}

// Refund puts tokens back. The balance is capped at capacity so a refund
// cannot raise the burst size. The timestamp is left alone: the next TryTake
// still applies the refill for the time that passed.
func (b *Bucket) Refund(n float64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens += n
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
}

func (b *Bucket) refill(now time.Time) {
	if !now.After(b.updated) {
		return
	}
	elapsed := now.Sub(b.updated).Seconds()
	b.updated = now
	if b.rate <= 0 {
		return
	}
	b.tokens += elapsed * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
}

// durationUntil is the wait until deficit tokens have dripped in. It rounds
// up so a caller that waits the full duration actually has enough.
func durationUntil(deficit, perSecond float64) time.Duration {
	if deficit <= 0 || perSecond <= 0 {
		return 0
	}
	nanos := deficit / perSecond * float64(time.Second)
	if nanos < 1 {
		return time.Nanosecond
	}
	return time.Duration(math.Ceil(nanos))
}
