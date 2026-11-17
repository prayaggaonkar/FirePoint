package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prayaggaonkar/firerouter/internal/breaker"
	"github.com/prayaggaonkar/firerouter/internal/observability"
)

func TestSlowHeadersWithinTimeoutAreServed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A non-streaming completion sends nothing until generation is done.
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	h, err := NewHandler(upstream.URL, NewClientWithHeaderTimeout(3*time.Second), "")
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	resp := postChat(t, proxy.URL, "", `{"model":"m"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
}

func TestHeadersPastTimeoutCountAsFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()

	h, err := NewHandler(upstream.URL, NewClientWithHeaderTimeout(100*time.Millisecond), "")
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	resp := postChat(t, proxy.URL, "", `{"model":"m"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
}

func TestDefaultHeaderTimeoutIsLongEnoughForRealModels(t *testing.T) {
	if got := NewTransport(0).ResponseHeaderTimeout; got != DefaultHeaderTimeout {
		t.Fatalf("zero timeout gave %v, want default %v", got, DefaultHeaderTimeout)
	}
	if DefaultHeaderTimeout < 30*time.Second {
		t.Fatalf("default %v is too short for non-streaming completions", DefaultHeaderTimeout)
	}
}

func TestStreamCutMidwayGetsErrorEventAndTripsBreaker(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\n")
		w.(http.Flusher).Flush()
		// Aborting closes the connection without the final chunk, as a crash would.
		panic(http.ErrAbortHandler)
	}))
	defer upstream.Close()

	h, err := NewHandler(upstream.URL, NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := breaker.DefaultConfig()
	cfg.MinRequests = 1
	reg := breaker.NewRegistry(cfg)
	h.SetBreakers(reg)
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	resp := postChat(t, proxy.URL, "", `{"model":"m","stream":true}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	text := string(body)
	if !strings.Contains(text, `"choices"`) {
		t.Fatalf("first chunk was not forwarded: %q", text)
	}
	if !strings.Contains(text, "upstream_stream_error") {
		t.Fatalf("no error event after the cut: %q", text)
	}
	if got := reg.Get("m").State(); got != breaker.Open {
		t.Fatalf("breaker state %v, want open after a failed stream", got)
	}
}

func TestCompleteStreamKeepsBreakerClosed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	h, err := NewHandler(upstream.URL, NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := breaker.DefaultConfig()
	cfg.MinRequests = 1
	reg := breaker.NewRegistry(cfg)
	h.SetBreakers(reg)
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	resp := postChat(t, proxy.URL, "", `{"model":"m","stream":true}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "upstream_stream_error") {
		t.Fatalf("clean stream got an error event: %q", body)
	}
	if got := reg.Get("m").State(); got != breaker.Closed {
		t.Fatalf("breaker state %v, want closed", got)
	}
}

func TestOpenModeRateLimitLabelDoesNotLeakKeyHashes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	m := observability.New()
	h := newMetricsHandler(t, upstream.URL, m)
	h.SetLimiter(1, 1000000)
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	for _, key := range []string{"Bearer a", "Bearer a", "Bearer b", "Bearer b"} {
		postChat(t, proxy.URL, key, `{"model":"m"}`).Body.Close()
	}
	var sb strings.Builder
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	sb.WriteString(rec.Body.String())
	if !strings.Contains(sb.String(), `firerouter_ratelimit_rejections_total{tenant="anonymous"} 2`) {
		t.Fatalf("want two rejections under tenant=anonymous:\n%s", sb.String())
	}
}

func TestEvictIdleWithoutLimiterIsSafe(t *testing.T) {
	h, err := NewHandler("http://127.0.0.1", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	h.EvictIdle()
}
