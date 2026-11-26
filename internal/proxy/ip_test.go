package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prayaggaonkar/firepoint/internal/ratelimit"
)

func TestClientIPPrefersFlyHeaderAndIgnoresForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "1.1.1.1")
	r.Header.Set("Fly-Client-IP", "203.0.113.9")
	if got := ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("ip = %q", got)
	}
	r.Header.Del("Fly-Client-IP")
	if got := ClientIP(r); got != "10.0.0.1" {
		t.Fatalf("peer = %q", got)
	}
}

func TestLimitByIPSkipsHealthAndRejectsTheRest(t *testing.T) {
	now := time.Unix(0, 0)
	lim := ratelimit.NewIPLimiter(1)
	var rejects int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := LimitByIP(lim, func() time.Time { return now }, func() { rejects++ }, next)

	health := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	health.RemoteAddr = "203.0.113.4:1"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, health)
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz %d", rr.Code)
	}

	first := httptest.NewRequest(http.MethodGet, "/", nil)
	first.RemoteAddr = "203.0.113.4:1"
	first.Header.Set("X-Forwarded-For", "198.51.100.2")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, first)
	if rr.Code != http.StatusOK {
		t.Fatalf("first %d", rr.Code)
	}

	// A forged forwarding header must not mint a second budget.
	second := httptest.NewRequest(http.MethodGet, "/", nil)
	second.RemoteAddr = "203.0.113.4:1"
	second.Header.Set("X-Forwarded-For", "198.51.100.9")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, second)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second %d", rr.Code)
	}
	if rejects != 1 {
		t.Fatalf("rejects = %d", rejects)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}

	again := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	again.RemoteAddr = "203.0.113.4:1"
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, again)
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz after limit %d", rr.Code)
	}
}
