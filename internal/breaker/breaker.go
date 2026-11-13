package breaker

import (
	"errors"
	"sync"
	"time"
)

// ErrOpen means the breaker is not letting calls through. The caller should
// fail fast instead of waiting on a broken upstream.
var ErrOpen = errors.New("circuit breaker open")

// State is the breaker's switch position.
type State int

const (
	// Closed lets every call through and watches the error rate.
	Closed State = iota
	// Open rejects calls until the cooldown has passed.
	Open
	// HalfOpen lets a few probes through to see if the upstream recovered.
	HalfOpen
)

func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Config is the trip and recovery policy.
type Config struct {
	Window         time.Duration // rolling sample, default 30s
	MinRequests    int           // do not trip before this many samples
	ErrorThreshold float64       // trip when the error rate reaches this
	OpenDuration   time.Duration // how long Open lasts before probes
	HalfOpenProbes int           // concurrent probes, and how many must succeed
}

// DefaultConfig is the policy used unless a caller overrides it.
func DefaultConfig() Config {
	return Config{
		Window:         30 * time.Second,
		MinRequests:    20,
		ErrorThreshold: 0.5,
		OpenDuration:   15 * time.Second,
		HalfOpenProbes: 3,
	}
}

// Breaker stops calling an upstream that is failing. Time is passed in so
// tests can move the cooldown forward without sleeping.
type Breaker struct {
	mu         sync.Mutex
	cfg        Config
	win        *Window
	state      State
	openedAt   time.Time
	generation int
	inflight   int
	successes  int
}

// New starts Closed. A zero Window or probe count is replaced with the default
// so a partial config cannot make every call look open.
func New(cfg Config) *Breaker {
	def := DefaultConfig()
	if cfg.Window < time.Second {
		cfg.Window = def.Window
	}
	if cfg.MinRequests < 1 {
		cfg.MinRequests = def.MinRequests
	}
	if cfg.ErrorThreshold <= 0 {
		cfg.ErrorThreshold = def.ErrorThreshold
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = def.OpenDuration
	}
	if cfg.HalfOpenProbes < 1 {
		cfg.HalfOpenProbes = def.HalfOpenProbes
	}
	return &Breaker{
		cfg:   cfg,
		win:   NewWindow(cfg.Window),
		state: Closed,
	}
}

// State returns the current position.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Allow either returns a callback the caller must run with the outcome, or
// ErrOpen when the call must not be sent. The callback records the outcome at
// the same instant Allow saw, which is the injected clock.
func (b *Breaker) Allow(now time.Time) (func(success bool), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.advance(now)

	switch b.state {
	case Open:
		return nil, ErrOpen
	case HalfOpen:
		if b.inflight >= b.cfg.HalfOpenProbes {
			return nil, ErrOpen
		}
		b.inflight++
		gen := b.generation
		var once sync.Once
		return func(success bool) {
			once.Do(func() { b.finishProbe(gen, success, now) })
		}, nil
	default:
		var once sync.Once
		return func(success bool) {
			once.Do(func() { b.finishClosed(success, now) })
		}, nil
	}
}

func (b *Breaker) advance(now time.Time) {
	if b.state != Open {
		return
	}
	if now.Before(b.openedAt.Add(b.cfg.OpenDuration)) {
		return
	}
	b.state = HalfOpen
	b.generation++
	b.inflight = 0
	b.successes = 0
}

func (b *Breaker) finishClosed(success bool, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != Closed {
		return
	}
	b.win.Add(success, now)
	if b.win.Total(now) < b.cfg.MinRequests {
		return
	}
	if b.win.ErrorRate(now) >= b.cfg.ErrorThreshold {
		b.trip(now)
	}
}

func (b *Breaker) finishProbe(gen int, success bool, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if gen != b.generation || b.state != HalfOpen {
		return
	}
	if b.inflight > 0 {
		b.inflight--
	}
	if !success {
		// One failed probe means the upstream is still down. Reject again
		// instead of waiting for the other probes.
		b.trip(now)
		return
	}
	b.successes++
	// Wait until every probe has finished. Closing while one is still out
	// would hide a failure that has not been reported yet.
	if b.inflight == 0 && b.successes >= b.cfg.HalfOpenProbes {
		b.close()
	}
}

func (b *Breaker) trip(now time.Time) {
	b.state = Open
	b.openedAt = now
	b.generation++
	b.inflight = 0
	b.successes = 0
}

func (b *Breaker) close() {
	b.state = Closed
	b.generation++
	b.inflight = 0
	b.successes = 0
	// The probes succeeded, so the errors that tripped the breaker are stale.
	b.win.Reset()
}
