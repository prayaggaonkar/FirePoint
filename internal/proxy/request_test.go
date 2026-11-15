package proxy

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prayaggaonkar/firerouter/internal/breaker"
)

func TestUpstream429PassesThrough(t *testing.T) {
	var hits atomic.Int32
	const errBody = `{"error":{"message":"slow down","type":"rate_limit_exceeded"}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, errBody)
	}))
	defer upstream.Close()

	h := newFallbackHandler(t, upstream.URL, "big", "small")
	cfg := breaker.DefaultConfig()
	cfg.MinRequests = 1
	cfg.ErrorThreshold = 0.5
	reg := breaker.NewRegistry(cfg)
	h.SetBreakers(reg)
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	for i := 0; i < 2; i++ {
		resp := postChat(t, proxy.URL, "", `{"model":"big","messages":[{"role":"user","content":"hi"}]}`)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status %d body %s", resp.StatusCode, body)
		}
		if resp.Header.Get("Retry-After") != "7" {
			t.Fatalf("Retry-After %q", resp.Header.Get("Retry-After"))
		}
		if strings.TrimSpace(string(body)) != errBody {
			t.Fatalf("body = %s", body)
		}
		if resp.Header.Get("X-FireRouter-Model") != "big" {
			t.Fatalf("fell back to %q", resp.Header.Get("X-FireRouter-Model"))
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("upstream hits %d, want 2 (no fallback, breaker still closed)", hits.Load())
	}
	if reg.Get("big").State() != breaker.Closed {
		t.Fatalf("breaker state %s, want closed", reg.Get("big").State())
	}
}

func TestRequestIDIsForwardedAndLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(flags)
	})

	var gotID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("X-Request-ID")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL)
	const prompt = "super-secret-prompt-zz"
	resp := postChat(t, proxy.URL, "", `{"model":"m","messages":[{"role":"user","content":"`+prompt+`"}]}`)
	resp.Body.Close()
	if gotID == "" || resp.Header.Get("X-FireRouter-Request-ID") != gotID {
		t.Fatalf("generated id upstream %q response %q", gotID, resp.Header.Get("X-FireRouter-Request-ID"))
	}
	if !strings.Contains(buf.String(), gotID) {
		t.Fatalf("log %q does not contain the request id", buf.String())
	}
	if strings.Contains(buf.String(), prompt) {
		t.Fatalf("log contains the prompt: %s", buf.String())
	}

	buf.Reset()
	gotID = ""
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "trace-123")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotID != "trace-123" || resp.Header.Get("X-FireRouter-Request-ID") != "trace-123" {
		t.Fatalf("forwarded id upstream %q response %q", gotID, resp.Header.Get("X-FireRouter-Request-ID"))
	}
	if !strings.Contains(buf.String(), "trace-123") {
		t.Fatalf("log %q missing caller id", buf.String())
	}
}

func TestInjectUsageRefundsOnlyWhenEnabled(t *testing.T) {
	const body = `{"model":"m","stream":true,"future_field":{"keep":true},"messages":[{"content":""}],"max_tokens":8}`
	// Estimate is 4 + max_tokens 8 = 12. A budget of 14 allows one call with
	// nothing left over, and a second call only if 10 tokens were refunded.
	for _, on := range []bool{false, true} {
		var seen []byte
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf, _ := io.ReadAll(r.Body)
			seen = append([]byte(nil), buf...)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"usage":{"total_tokens":2}}`)
		}))
		h, err := NewHandler(upstream.URL, NewClient(), "")
		if err != nil {
			t.Fatal(err)
		}
		h.SetInjectUsage(on)
		h.SetLimiter(100, 14)
		proxy := httptest.NewServer(h)
		first := postChat(t, proxy.URL, "", body)
		first.Body.Close()
		if first.StatusCode != http.StatusOK {
			t.Fatalf("inject %v first status %d", on, first.StatusCode)
		}
		if on {
			if !strings.Contains(string(seen), `"include_usage":true`) || !strings.Contains(string(seen), `"future_field":{"keep":true}`) {
				t.Fatalf("forwarded body = %s", seen)
			}
		} else if strings.Contains(string(seen), "include_usage") {
			t.Fatalf("flag off still injected usage: %s", seen)
		}
		second := postChat(t, proxy.URL, "", body)
		second.Body.Close()
		upstream.Close()
		proxy.Close()
		if on && second.StatusCode != http.StatusOK {
			t.Fatalf("inject on: second status %d, want 200 after refund", second.StatusCode)
		}
		if !on && second.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("inject off: second status %d, want 429 because the estimate was kept", second.StatusCode)
		}
	}
}
