package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOverheadExcludesUpstreamWait(t *testing.T) {
	received := time.Unix(0, 0)
	sent := received.Add(2 * time.Millisecond)
	upstreamFirst := sent.Add(200 * time.Millisecond)
	forwarded := upstreamFirst.Add(time.Millisecond)
	got := Overhead(received, sent, upstreamFirst, forwarded)
	want := 3 * time.Millisecond
	if got != want {
		t.Fatalf("overhead = %s, want %s", got, want)
	}
}

func TestOverheadIgnoresClockSkew(t *testing.T) {
	base := time.Unix(50, 0)
	if got := Overhead(base, base.Add(-time.Second), base, base.Add(-time.Millisecond)); got != 0 {
		t.Fatalf("overhead = %s, want 0", got)
	}
}

func TestHandlerExportsNames(t *testing.T) {
	m := New()
	m.ObserveTTFT(100 * time.Millisecond)
	m.ObserveOverhead(time.Millisecond)
	m.AddStreams(1)
	m.UpstreamStatus("200")
	m.RateLimited("alice")
	m.Served("small")
	m.SetBreakerState("small", 0)

	body := scrape(t, m.Handler())
	for _, name := range []string{
		"firepoint_ttft_seconds",
		"firepoint_proxy_overhead_seconds",
		"firepoint_active_streams 1",
		`firepoint_upstream_status_total{status="200"} 1`,
		`firepoint_ratelimit_rejections_total{tenant="alice"} 1`,
		`firepoint_requests_total{model="small"} 1`,
		`firepoint_breaker_state{model="small"} 0`,
	} {
		if !strings.Contains(body, name) {
			t.Errorf("scrape missing %s", name)
		}
	}
	if strings.Contains(body, "Bearer") || strings.Contains(body, "sk-") {
		t.Fatalf("metrics included a credential:\n%s", body)
	}
}

func TestNilMetricsAreSafe(t *testing.T) {
	var m *Metrics
	m.ObserveTTFT(time.Second)
	m.ObserveOverhead(time.Second)
	m.AddStreams(1)
	m.UpstreamStatus("200")
	m.RateLimited("alice")
	m.Served("m")
	m.SetBreakerState("m", 1)
	m.IPLimited()
	_ = m.Summary()
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status %d", rr.Code)
	}
}

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestModelLabelsAreCapped(t *testing.T) {
	m := New()
	for i := 0; i < maxLabelValues+50; i++ {
		m.Served("model-" + strings.Repeat("x", i))
	}
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	series := strings.Count(body, "\nfirepoint_requests_total{")
	if series != maxLabelValues+1 {
		t.Fatalf("series = %d, want %d (cap plus %q)", series, maxLabelValues+1, overflowLabel)
	}
	if !strings.Contains(body, `model="other"} 50`) {
		t.Fatalf("overflow bucket missing or wrong:\n%s", body)
	}
}
