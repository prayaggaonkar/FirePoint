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
	m.SetBreakerState("small", 0)

	body := scrape(t, m.Handler())
	for _, name := range []string{
		"firerouter_ttft_seconds",
		"firerouter_proxy_overhead_seconds",
		"firerouter_active_streams 1",
		`firerouter_upstream_status_total{status="200"} 1`,
		`firerouter_ratelimit_rejections_total{tenant="alice"} 1`,
		`firerouter_breaker_state{model="small"} 0`,
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
	m.SetBreakerState("m", 1)
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
