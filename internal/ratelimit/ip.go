package ratelimit

import (
	"sync"
	"time"
)

// IPLimiter is one request bucket per client address.
// Time is supplied by the caller so tests can move the clock without sleeping.
type IPLimiter struct {
	rpm     float64
	mu      sync.Mutex
	buckets map[string]*ipEntry
	swept   time.Time
}

type ipEntry struct {
	bucket   *Bucket
	lastSeen time.Time
}

// NewIPLimiter builds a limiter whose burst and sustained rate are both
// requestsPerMinute. Values below 1 are treated as 1 so a public deploy
// cannot accidentally turn the limit off by passing a bad flag.
func NewIPLimiter(requestsPerMinute int) *IPLimiter {
	if requestsPerMinute < 1 {
		requestsPerMinute = 1
	}
	rpm := float64(requestsPerMinute)
	return &IPLimiter{
		rpm:     rpm,
		buckets: map[string]*ipEntry{},
	}
}

// Allow takes one request from the address's bucket.
// An empty address shares the "unknown" bucket, so a missing client IP
// cannot mint a new budget on every call.
func (l *IPLimiter) Allow(ip string, now time.Time) (bool, time.Duration) {
	if ip == "" {
		ip = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.swept.IsZero() || now.Sub(l.swept) >= time.Minute {
		for id, ent := range l.buckets {
			if now.Sub(ent.lastSeen) > idleAfter {
				delete(l.buckets, id)
			}
		}
		l.swept = now
	}
	ent := l.buckets[ip]
	if ent == nil {
		ent = &ipEntry{bucket: NewBucket(l.rpm, l.rpm/60, now)}
		l.buckets[ip] = ent
	}
	ent.lastSeen = now
	return ent.bucket.TryTake(1, now)
}

func (l *IPLimiter) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
