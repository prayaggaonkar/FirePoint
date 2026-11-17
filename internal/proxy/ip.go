package proxy

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prayaggaonkar/firerouter/internal/ratelimit"
)

// ClientIP is the address the per-IP limit applies to.
// Fly overwrites Fly-Client-IP, so that header is the client when it is set.
// X-Forwarded-For is ignored: the caller can put any value there and would
// otherwise receive a fresh budget for each one.
func ClientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("Fly-Client-IP")); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// LimitByIP applies lim to every path except /healthz.
// Health checks come from the platform and must keep working after a
// visitor has used the public budget. now may be nil, which means time.Now.
// onReject may be nil; it runs when a request is refused.
func LimitByIP(lim *ratelimit.IPLimiter, now func() time.Time, onReject func(), next http.Handler) http.Handler {
	if lim == nil || next == nil {
		return next
	}
	if now == nil {
		now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		ok, retry := lim.Allow(ClientIP(r), now())
		if !ok {
			if onReject != nil {
				onReject()
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retry)))
			http.Error(w, "too many requests from this address", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
