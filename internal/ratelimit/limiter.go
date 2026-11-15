package ratelimit

import (
	"hash/fnv"
	"sync"
	"time"
)

const (
	shardCount = 64
	idleAfter  = 10 * time.Minute
)

// Limiter enforces a request-per-minute bucket and a token-per-minute bucket
// for each tenant. Fireworks limits the whole account; this stops one tenant
// from using that shared quota.
type Limiter struct {
	rpm    float64
	tpm    float64
	shards [shardCount]shard
}

type shard struct {
	mu      sync.Mutex
	tenants map[string]*tenant
}

type tenant struct {
	rpm      *Bucket
	tpm      *Bucket
	lastSeen time.Time
}

// NewLimiter builds an empty limiter. requestsPerMinute and tokensPerMinute
// are both the burst size and the sustained rate. Each tenant map is split
// into 64 shards so one busy tenant does not lock every other tenant.
func NewLimiter(requestsPerMinute, tokensPerMinute int) *Limiter {
	if requestsPerMinute < 0 {
		requestsPerMinute = 0
	}
	if tokensPerMinute < 0 {
		tokensPerMinute = 0
	}
	l := &Limiter{
		rpm: float64(requestsPerMinute),
		tpm: float64(tokensPerMinute),
	}
	for i := range l.shards {
		l.shards[i].tenants = make(map[string]*tenant)
	}
	return l
}

// Allow takes one request token, then the estimated LLM tokens, using the
// limiter's shared rates. The token take can fail after the request take
// succeeded. The request token is returned in that case so a rejected call
// does not count against RPM.
func (l *Limiter) Allow(tenantID string, estimatedTokens int, now time.Time) (ok bool, retryAfter time.Duration) {
	return l.allow(tenantID, l.rpm, l.tpm, estimatedTokens, now)
}

// AllowTenant is Allow with the rates from that tenant's key entry.
// A later call with new rates updates the buckets in place. The balance
// is not refilled, so a SIGHUP does not grant a fresh burst.
func (l *Limiter) AllowTenant(tenantID string, rpm, tpm, estimatedTokens int, now time.Time) (ok bool, retryAfter time.Duration) {
	if rpm < 0 {
		rpm = 0
	}
	if tpm < 0 {
		tpm = 0
	}
	return l.allow(tenantID, float64(rpm), float64(tpm), estimatedTokens, now)
}

func (l *Limiter) allow(tenantID string, rpm, tpm float64, estimatedTokens int, now time.Time) (ok bool, retryAfter time.Duration) {
	if estimatedTokens < 0 {
		estimatedTokens = 0
	}
	sh := l.shard(tenantID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	t := sh.tenants[tenantID]
	if t == nil {
		// A per-minute limit is a per-second drip of limit/60, with a burst
		// equal to one minute of tokens.
		t = &tenant{
			rpm: NewBucket(rpm, rpm/60, now),
			tpm: NewBucket(tpm, tpm/60, now),
		}
		sh.tenants[tenantID] = t
	} else {
		t.rpm.SetRates(rpm, rpm/60)
		t.tpm.SetRates(tpm, tpm/60)
	}
	t.lastSeen = now

	ok, retryAfter = t.rpm.TryTake(1, now)
	if !ok {
		return false, retryAfter
	}
	ok, retryAfter = t.tpm.TryTake(float64(estimatedTokens), now)
	if !ok {
		t.rpm.Refund(1)
		return false, retryAfter
	}
	return true, 0
}

// Refund returns unused LLM tokens after the real usage arrives. It does not
// return the request token: the request itself already happened.
func (l *Limiter) Refund(tenantID string, tokens int) {
	if tokens <= 0 {
		return
	}
	sh := l.shard(tenantID)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	t := sh.tenants[tenantID]
	if t == nil {
		return
	}
	t.tpm.Refund(float64(tokens))
}

// EvictIdle drops tenants with no Allow for more than 10 minutes so the map
// does not keep a bucket pair for every customer that ever called.
func (l *Limiter) EvictIdle(now time.Time) {
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		for id, t := range sh.tenants {
			if now.Sub(t.lastSeen) > idleAfter {
				delete(sh.tenants, id)
			}
		}
		sh.mu.Unlock()
	}
}

func (l *Limiter) shard(tenantID string) *shard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(tenantID))
	return &l.shards[h.Sum32()%shardCount]
}
