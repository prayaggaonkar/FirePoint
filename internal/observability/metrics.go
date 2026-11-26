// Package observability publishes Prometheus metrics for the proxy.
// Label values are model names and status codes. Prompts and API keys are never labels.
package observability

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the set of series scraped from GET /metrics.
// A nil Metrics ignores every method so handlers can record unconditionally.
type Metrics struct {
	reg            *prometheus.Registry
	ttft           prometheus.Histogram
	overhead       prometheus.Histogram
	active         prometheus.Gauge
	upstreamStatus *prometheus.CounterVec
	rateLimited    *prometheus.CounterVec
	served         *prometheus.CounterVec
	breakerState   *prometheus.GaugeVec
	ipLimited      prometheus.Counter
	models         labelCap
	tenants        labelCap
}

// maxLabelValues bounds how many distinct model or tenant names become label
// values. The model name comes from the caller, so without a cap a client
// could create a new time series per request and exhaust memory.
const maxLabelValues = 64

// overflowLabel replaces any value that arrives after the cap is reached.
const overflowLabel = "other"

type labelCap struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func (c *labelCap) value(v string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[v]; ok {
		return v
	}
	if len(c.seen) >= maxLabelValues {
		return overflowLabel
	}
	if c.seen == nil {
		c.seen = make(map[string]struct{})
	}
	c.seen[v] = struct{}{}
	return v
}

// New registers the FirePoint series on a private registry.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		ttft: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "firepoint_ttft_seconds",
			Help:    "Seconds from when the proxy received the request until it forwarded the first response byte.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 1, 2, 5, 10},
		}),
		overhead: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "firepoint_proxy_overhead_seconds",
			Help:    "Seconds the proxy itself added: from request received to upstream request sent, plus from the upstream first byte to the first byte forwarded.",
			Buckets: []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}),
		active: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "firepoint_active_streams",
			Help: "Event streams currently being forwarded to clients.",
		}),
		upstreamStatus: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "firepoint_upstream_status_total",
			Help: "Upstream responses by HTTP status. Transport failures use status \"error\".",
		}, []string{"status"}),
		rateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "firepoint_ratelimit_rejections_total",
			Help: "Requests rejected by the per-client rate limiter.",
		}, []string{"tenant"}),
		served: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "firepoint_requests_total",
			Help: "Responses committed to a client, labeled by the model that served them.",
		}, []string{"model"}),
		breakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "firepoint_breaker_state",
			Help: "Circuit breaker position for one model: 0 closed, 1 open, 2 half-open.",
		}, []string{"model"}),
		ipLimited: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "firepoint_ip_rate_limit_rejections_total",
			Help: "Requests rejected by the per-address rate limiter. The address is not a label.",
		}),
	}
	reg.MustRegister(m.ttft, m.overhead, m.active, m.upstreamStatus, m.rateLimited, m.served, m.breakerState, m.ipLimited)
	return m
}

// Handler serves the Prometheus text exposition.
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// ObserveTTFT records one time-to-first-byte sample.
func (m *Metrics) ObserveTTFT(d time.Duration) {
	if m == nil || d < 0 {
		return
	}
	m.ttft.Observe(d.Seconds())
}

// ObserveOverhead records one proxy-overhead sample.
func (m *Metrics) ObserveOverhead(d time.Duration) {
	if m == nil || d < 0 {
		return
	}
	m.overhead.Observe(d.Seconds())
}

// AddStreams changes the number of event streams being forwarded.
func (m *Metrics) AddStreams(n float64) {
	if m == nil || n == 0 {
		return
	}
	m.active.Add(n)
}

// UpstreamStatus counts one upstream response. status is an HTTP code or "error".
func (m *Metrics) UpstreamStatus(status string) {
	if m == nil || status == "" {
		return
	}
	m.upstreamStatus.WithLabelValues(status).Inc()
}

// RateLimited counts one rejected request. tenant is the tenant name, which
// is not a secret. An empty name is recorded as "unknown".
func (m *Metrics) RateLimited(tenant string) {
	if m == nil {
		return
	}
	if tenant == "" {
		tenant = "unknown"
	}
	m.rateLimited.WithLabelValues(m.tenants.value(tenant)).Inc()
}

// IPLimited counts one request refused by the per-address limiter.
// The address itself is not recorded.
func (m *Metrics) IPLimited() {
	if m == nil {
		return
	}
	m.ipLimited.Inc()
}

// Served counts one response that was committed to the client.
// model is the candidate that produced it, not the name the client asked for.
func (m *Metrics) Served(model string) {
	if m == nil || model == "" {
		return
	}
	m.served.WithLabelValues(m.models.value(model)).Inc()
}

// SetBreakerState records the breaker position for model.
// value is 0 closed, 1 open, 2 half-open, matching breaker.State.
func (m *Metrics) SetBreakerState(model string, value float64) {
	if m == nil {
		return
	}
	m.breakerState.WithLabelValues(m.models.value(model)).Set(value)
}

// Overhead is the proxy's own time for one served response.
// It is the wait before the upstream request is sent, plus the wait to
// forward the first byte after the upstream has already produced it.
// Time the upstream spends generating that byte is not included.
func Overhead(received, sent, upstreamFirst, forwarded time.Time) time.Duration {
	pre := sent.Sub(received)
	post := forwarded.Sub(upstreamFirst)
	if pre < 0 {
		pre = 0
	}
	if post < 0 {
		post = 0
	}
	return pre + post
}
