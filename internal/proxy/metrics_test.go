package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prayaggaonkar/firepoint/internal/breaker"
	"github.com/prayaggaonkar/firepoint/internal/observability"
)

func TestMetricsSeparateOverheadFromUpstreamWait(t *testing.T) {
	const pause = 80 * time.Millisecond
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		time.Sleep(pause)
		_, _ = io.WriteString(w, "data: hi\n\n")
		_ = rc.Flush()
	}))
	defer upstream.Close()

	m := observability.New()
	h := newMetricsHandler(t, upstream.URL, m)
	h.SetBreakers(breaker.NewRegistry(breaker.DefaultConfig()))
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	resp := postChat(t, proxy.URL, "Bearer sk-test-secret", `{"model":"metrics-model","messages":[{"role":"user","content":"super-secret-prompt-text"}]}`)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	body := scrapeMetrics(t, m)
	if strings.Contains(body, "sk-test-secret") || strings.Contains(body, "super-secret-prompt-text") {
		t.Fatal("metrics included the prompt or the API key")
	}
	ttft := metricValue(t, body, "firepoint_ttft_seconds_sum")
	overhead := metricValue(t, body, "firepoint_proxy_overhead_seconds_sum")
	if ttft < pause.Seconds()/2 {
		t.Fatalf("ttft %fs, upstream paused %s", ttft, pause)
	}
	// The pause is upstream time. Overhead is only the proxy's own gaps.
	if overhead >= ttft/2 {
		t.Fatalf("overhead %fs is not smaller than ttft %fs", overhead, ttft)
	}
	if got := metricValue(t, body, `firepoint_upstream_status_total{status="200"}`); got != 1 {
		t.Fatalf("status counter %v", got)
	}
	if got := metricValue(t, body, `firepoint_requests_total{model="metrics-model"}`); got != 1 {
		t.Fatalf("served requests %v", got)
	}
	if got := metricValue(t, body, `firepoint_breaker_state{model="metrics-model"}`); got != float64(breaker.Closed) {
		t.Fatalf("breaker state %v", got)
	}
	if got := metricValue(t, body, "firepoint_active_streams"); got != 0 {
		t.Fatalf("active streams %v after the response finished", got)
	}
}

func TestMetricsCountRateLimitBreakerAndActiveStream(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_, _ = io.WriteString(w, "data: hi\n\n")
		_ = rc.Flush()
		close(entered)
		<-release
	}))
	defer upstream.Close()

	m := observability.New()
	h := newMetricsHandler(t, upstream.URL, m)
	h.SetLimiter(1, 100000)
	cfg := breaker.DefaultConfig()
	cfg.MinRequests = 1
	h.SetBreakers(breaker.NewRegistry(cfg))
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	errCh := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(`{"model":"big","stream":true}`))
		if err != nil {
			errCh <- err
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer tenant-a")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		_, err = io.Copy(io.Discard, resp.Body)
		errCh <- err
	}()
	select {
	case <-entered:
	case err := <-errCh:
		t.Fatalf("stream ended before the first byte: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the stream to start")
	}
	deadline := time.Now().Add(2 * time.Second)
	var active float64
	var found bool
	for {
		active, found = metricValueOK(scrapeMetrics(t, m), "firepoint_active_streams")
		if found && active == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !found || active != 1 {
		t.Fatalf("active streams %v found %v while the body is open", active, found)
	}
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if got := metricValue(t, scrapeMetrics(t, m), "firepoint_active_streams"); got != 0 {
		t.Fatalf("active streams %v after close", got)
	}

	limited := postChat(t, proxy.URL, "Bearer tenant-a", `{"model":"big"}`)
	limited.Body.Close()
	if limited.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", limited.StatusCode)
	}
	if got := metricValue(t, scrapeMetrics(t, m), "firepoint_ratelimit_rejections_total"); got != 1 {
		t.Fatalf("rejections %v", got)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	h2 := newMetricsHandler(t, failing.URL, m)
	cfg = breaker.DefaultConfig()
	cfg.MinRequests = 1
	h2.SetBreakers(breaker.NewRegistry(cfg))
	proxy2 := httptest.NewServer(h2)
	defer proxy2.Close()
	resp := postChat(t, proxy2.URL, "", `{"model":"big"}`)
	resp.Body.Close()
	body := scrapeMetrics(t, m)
	if got := metricValue(t, body, `firepoint_upstream_status_total{status="503"}`); got != 1 {
		t.Fatalf("503 counter %v", got)
	}
	if got := metricValue(t, body, `firepoint_breaker_state{model="big"}`); got != float64(breaker.Open) {
		t.Fatalf("breaker state %v, want open (%v)", got, float64(breaker.Open))
	}
}

func newMetricsHandler(t *testing.T, upstream string, m *observability.Metrics) *Handler {
	t.Helper()
	h, err := NewHandler(upstream, NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	h.SetMetrics(m)
	return h
}

func scrapeMetrics(t *testing.T, m *observability.Metrics) string {
	t.Helper()
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("metrics status %d", rr.Code)
	}
	return rr.Body.String()
}

func metricValue(t *testing.T, body, name string) float64 {
	t.Helper()
	v, ok := metricValueOK(body, name)
	if !ok {
		t.Fatalf("metric %s not in scrape", name)
	}
	return v
}

func metricValueOK(body, name string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		rest := strings.TrimPrefix(line, name)
		if rest != "" && !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "{") {
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}
