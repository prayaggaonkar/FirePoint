package breaker

import (
	"sync"
	"time"
)

// Registry keeps one breaker per model. Models do not share a window: an
// outage on one name must not open the breaker for its fallback.
type Registry struct {
	mu  sync.Mutex
	cfg Config
	m   map[string]*Breaker
}

// NewRegistry starts with no models. Each name is created on first use with cfg.
func NewRegistry(cfg Config) *Registry {
	return &Registry{cfg: cfg, m: make(map[string]*Breaker)}
}

// Get returns the breaker for model, creating it if needed.
func (r *Registry) Get(model string) *Breaker {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.m[model]
	if !ok {
		b = New(r.cfg)
		r.m[model] = b
	}
	return b
}

// Allow asks the breaker for model. The caller runs the callback once.
func (r *Registry) Allow(model string, now time.Time) (func(Outcome), error) {
	return r.Get(model).Allow(now)
}
