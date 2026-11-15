package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prayaggaonkar/firerouter/internal/breaker"
	"github.com/prayaggaonkar/firerouter/internal/ratelimit"
	"github.com/prayaggaonkar/firerouter/internal/router"
)

func TestChunksArriveAbout100msApart(t *testing.T) {
	const (
		gap       = 100 * time.Millisecond
		tolerance = 50 * time.Millisecond
		chunks    = 5
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := http.NewResponseController(w)
		for i := 0; i < chunks; i++ {
			time.Sleep(gap)
			if _, err := io.WriteString(w, "data: "+string(rune('0'+i))+"\n\n"); err != nil {
				return
			}
			if err := flusher.Flush(); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL)
	resp, err := http.Post(proxy.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var arrivals []time.Time
	for len(arrivals) < chunks {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read chunk %d: %v", len(arrivals), err)
		}
		if strings.HasPrefix(line, "data: ") {
			arrivals = append(arrivals, time.Now())
		}
	}
	if len(arrivals) != chunks {
		t.Fatalf("got %d chunks, want %d", len(arrivals), chunks)
	}
	for i := 1; i < len(arrivals); i++ {
		got := arrivals[i].Sub(arrivals[i-1])
		if got < gap-tolerance || got > gap+tolerance {
			t.Fatalf("gap %d = %s, want %s ± %s", i, got, gap, tolerance)
		}
	}
}

func TestClientCancelStopsUpstream(t *testing.T) {
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_, _ = io.WriteString(w, "data: hi\n\n")
		_ = rc.Flush()
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(2 * time.Second):
		}
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream context was not cancelled within 1s")
	}
}

func TestBodyPassesThroughAndClientAuthorizationDoesNot(t *testing.T) {
	const wantBody = `{"model":"test","messages":[{"role":"user","content":"hi"}]}`
	saw := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(saw)
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(got) != wantBody {
			t.Errorf("body = %q, want %q", got, wantBody)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, client key must not be forwarded", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(wantBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	select {
	case <-saw:
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive the request")
	}
}

func TestUpstreamReceivesUnknownFields(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		mu.Lock()
		bodies = append(bodies, got)
		mu.Unlock()
		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(got, &req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		if strings.Contains(req.Model, "broken") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	proxy := newFallbackProxy(t, upstream.URL, "big-broken", "small")
	resp := postChat(t, proxy.URL, "", richBody)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(bodies))
	}
	assertDecodedEqual(t, []byte(richBody), bodies[0])
	assertDecodedModel(t, []byte(richBody), bodies[1], "small")
}

func assertDecodedEqual(t *testing.T, want, got []byte) {
	t.Helper()
	var left, right map[string]any
	if err := json.Unmarshal(want, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &right); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("upstream body = %#v\nwant %#v", right, left)
	}
}

func assertDecodedModel(t *testing.T, original, got []byte, model string) {
	t.Helper()
	var want, have map[string]any
	if err := json.Unmarshal(original, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &have); err != nil {
		t.Fatal(err)
	}
	want["model"] = model
	if !reflect.DeepEqual(have, want) {
		t.Fatalf("upstream body = %#v\nwant %#v", have, want)
	}
}

func TestUpstreamURLKeepsPathPrefix(t *testing.T) {
	h, err := NewHandler("https://api.fireworks.ai/inference", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.upstreamRequestURL(&http.Request{URL: &url.URL{Path: "/v1/chat/completions"}})
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://api.fireworks.ai/inference/v1/chat/completions"
	if got != want {
		t.Fatalf("url = %s, want %s", got, want)
	}

	local, err := NewHandler("http://localhost:9000", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err = local.upstreamRequestURL(&http.Request{URL: &url.URL{Path: "/v1/chat/completions", RawQuery: "x=1"}})
	if err != nil {
		t.Fatal(err)
	}
	const localWant = "http://localhost:9000/v1/chat/completions?x=1"
	if got != localWant {
		t.Fatalf("url = %s, want %s", got, localWant)
	}
}

func TestGatewayKeyReplacesClientAuthorization(t *testing.T) {
	const gatewayKey = "gateway-secret"
	saw := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(saw)
		if got := r.URL.Path; got != "/inference/v1/chat/completions" {
			t.Errorf("path = %s, want /inference/v1/chat/completions", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+gatewayKey {
			t.Errorf("Authorization was not the gateway key")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	h, err := NewHandler(upstream.URL+"/inference", NewClient(), gatewayKey)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer client-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	select {
	case <-saw:
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive the request")
	}
}

func TestRequestsOverLimitReturn429(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	h, err := NewHandler(upstream.URL, NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	const limit = 3
	h.SetLimiter(limit, 200000)
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	for i := 0; i < limit; i++ {
		resp := postChat(t, proxy.URL, "Bearer tenant-a", `{"model":"m"}`)
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("request %d status %d body %s", i+1, resp.StatusCode, body)
		}
		resp.Body.Close()
	}

	resp := postChat(t, proxy.URL, "Bearer tenant-a", `{"model":"m"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", resp.StatusCode)
	}
	secs, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || secs <= 0 {
		t.Fatalf("Retry-After = %q", resp.Header.Get("Retry-After"))
	}
	var apiErr struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil {
		t.Fatal(err)
	}
	if apiErr.Error.Type != "rate_limit_exceeded" {
		t.Fatalf("error type %q", apiErr.Error.Type)
	}
	if strings.Contains(apiErr.Error.Message, "Bearer") || strings.Contains(apiErr.Error.Message, "tenant-a") {
		t.Fatal("rate limit error included the client credential")
	}
	if got := hits.Load(); got != limit {
		t.Fatalf("upstream hits %d, want %d", got, limit)
	}

	other := postChat(t, proxy.URL, "Bearer tenant-b", `{"model":"m"}`)
	defer other.Body.Close()
	if other.StatusCode != http.StatusOK {
		t.Fatalf("other tenant status %d, want 200", other.StatusCode)
	}
}

func TestUsageRefundLetsAnotherRequestThrough(t *testing.T) {
	body := `{"messages":[{"content":"hi"}],"max_tokens":100}`
	reserved := ratelimit.EstimateTokens([]byte(body))
	const actual = 10

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := http.NewResponseController(w)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		_ = flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2,\"total_tokens\":10}}\n\n")
		_ = flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		_ = flusher.Flush()
	}))
	defer upstream.Close()

	h, err := NewHandler(upstream.URL, NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	// One full reservation plus the tokens that should come back as a refund.
	// A second call fits only if reserved-actual was returned.
	h.SetLimiter(10, reserved+actual)
	h.SetInjectUsage(true)
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	first := postChat(t, proxy.URL, "Bearer tenant-a", body)
	io.Copy(io.Discard, first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status %d", first.StatusCode)
	}

	second := postChat(t, proxy.URL, "Bearer tenant-a", body)
	io.Copy(io.Discard, second.Body)
	second.Body.Close()
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second status %d, want 200 after refund", second.StatusCode)
	}

	third := postChat(t, proxy.URL, "Bearer tenant-a", body)
	defer third.Body.Close()
	if third.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third status %d, want 429", third.StatusCode)
	}
}

func TestBreakerOpensWhenUpstreamReturns503(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream down","type":"server_error"}}`)
	}))
	defer upstream.Close()

	h, err := NewHandler(upstream.URL, NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	h.SetBreaker(breaker.New(breaker.Config{
		Window:         30 * time.Second,
		MinRequests:    4,
		ErrorThreshold: 0.5,
		OpenDuration:   15 * time.Second,
		HalfOpenProbes: 3,
	}))
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	for i := 0; i < 4; i++ {
		resp := postChat(t, proxy.URL, "", `{"model":"m"}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("request %d status %d", i+1, resp.StatusCode)
		}
	}
	if got := hits.Load(); got != 4 {
		t.Fatalf("upstream hits before the breaker opened: %d", got)
	}

	resp := postChat(t, proxy.URL, "", `{"model":"m"}`)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
	if got := hits.Load(); got != 4 {
		t.Fatalf("open breaker still called upstream, hits %d", got)
	}
	var apiErr struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &apiErr); err != nil {
		t.Fatal(err)
	}
	if apiErr.Error.Type != "server_error" || apiErr.Error.Message != "The upstream is unavailable." {
		t.Fatalf("error = %+v", apiErr.Error)
	}
}

func TestClientAndBackoffStatusesDoNotOpenBreaker(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(code)
			}))
			defer upstream.Close()

			h, err := NewHandler(upstream.URL, NewClient(), "")
			if err != nil {
				t.Fatal(err)
			}
			h.SetBreaker(breaker.New(breaker.Config{
				Window:         30 * time.Second,
				MinRequests:    2,
				ErrorThreshold: 0.5,
				OpenDuration:   15 * time.Second,
				HalfOpenProbes: 3,
			}))
			proxy := httptest.NewServer(h)
			defer proxy.Close()

			for i := 0; i < 4; i++ {
				resp := postChat(t, proxy.URL, "", `{"model":"m"}`)
				resp.Body.Close()
				if resp.StatusCode != code {
					t.Fatalf("request %d status %d, want %d", i+1, resp.StatusCode, code)
				}
			}
			if got := hits.Load(); got != 4 {
				t.Fatalf("upstream hits %d, want 4", got)
			}
		})
	}
}

func TestUpstreamOutcome(t *testing.T) {
	tests := []struct {
		status int
		want   breaker.Outcome
	}{
		{http.StatusOK, breaker.Succeeded},
		{http.StatusBadRequest, breaker.Ignored},
		{http.StatusUnauthorized, breaker.Ignored},
		{http.StatusNotFound, breaker.Ignored},
		{http.StatusTooManyRequests, breaker.Backoff},
		{http.StatusInternalServerError, breaker.Failed},
		{http.StatusBadGateway, breaker.Failed},
		{http.StatusServiceUnavailable, breaker.Failed},
		{http.StatusGatewayTimeout, breaker.Failed},
	}
	for _, tc := range tests {
		if got := upstreamOutcome(tc.status); got != tc.want {
			t.Errorf("status %d outcome %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestActualTokensUsesTheTail(t *testing.T) {
	var tail tailBuf
	tail.write([]byte(`{"usage":{"total_tokens":1}}`))
	tail.write(bytes.Repeat([]byte("x"), usageTailBytes))
	if _, ok := actualTokens(tail.bytes()); ok {
		t.Fatal("usage older than 4KB was still visible")
	}
	tail.write([]byte("data: {\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4}}\n\n"))
	got, ok := actualTokens(tail.bytes())
	if !ok || got != 7 {
		t.Fatalf("actual = %d ok=%v, want 7", got, ok)
	}
}

func TestBrokenModelFallsBackToSmall(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		mu.Lock()
		seen = append(seen, req.Model)
		mu.Unlock()
		if strings.Contains(req.Model, "broken") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"down","type":"server_error"}}`)
			return
		}
		if len(req.Messages) != 1 || req.Messages[0].Content != "hi" {
			t.Errorf("messages = %+v", req.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"model": req.Model})
	}))
	defer upstream.Close()

	proxy := newFallbackProxy(t, upstream.URL, "big-broken", "small")
	resp := postChat(t, proxy.URL, "", `{"model":"big-broken","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, want 200, body %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-FireRouter-Model"); got != "small" {
		t.Fatalf("X-FireRouter-Model = %q, want small", got)
	}
	var served struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&served); err != nil {
		t.Fatal(err)
	}
	if served.Model != "small" {
		t.Fatalf("served model = %q, want small", served.Model)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "big-broken" || seen[1] != "small" {
		t.Fatalf("upstream models = %v, want [big-broken small]", seen)
	}
}

func TestDialErrorFallsBackBeforeHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		if strings.Contains(req.Model, "broken") {
			// Close before status bytes so the proxy still has a candidate left.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("upstream cannot hijack")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"model": req.Model})
	}))
	defer upstream.Close()

	proxy := newFallbackProxy(t, upstream.URL, "big-broken", "small")
	resp := postChat(t, proxy.URL, "", `{"model":"big-broken"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d, want 200, body %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-FireRouter-Model"); got != "small" {
		t.Fatalf("X-FireRouter-Model = %q, want small", got)
	}
}

func TestOpenBreakerSkipsToFallback(t *testing.T) {
	var bigHits, smallHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		if req.Model == "big" {
			bigHits.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		smallHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	h := newFallbackHandler(t, upstream.URL, "big", "small")
	cfg := breaker.DefaultConfig()
	cfg.MinRequests = 1
	h.SetBreakers(breaker.NewRegistry(cfg))
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	for i := 0; i < 2; i++ {
		resp := postChat(t, proxy.URL, "", `{"model":"big"}`)
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("request %d status %d, body %s", i+1, resp.StatusCode, body)
		}
		if got := resp.Header.Get("X-FireRouter-Model"); got != "small" {
			resp.Body.Close()
			t.Fatalf("X-FireRouter-Model = %q, want small", got)
		}
		resp.Body.Close()
	}
	if got := bigHits.Load(); got != 1 {
		t.Fatalf("big was called %d times, want 1", got)
	}
	if got := smallHits.Load(); got != 2 {
		t.Fatalf("small was called %d times, want 2", got)
	}
}

func TestAllCandidatesFailReturns503(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	proxy := newFallbackProxy(t, upstream.URL, "big-broken", "small")
	resp := postChat(t, proxy.URL, "", `{"model":"big-broken"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("X-FireRouter-Model"); got != "" {
		t.Fatalf("X-FireRouter-Model = %q, want empty", got)
	}
}

func TestUpstream4xxIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "big" {
			t.Errorf("fallback model %s was called", req.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad","type":"invalid_request_error"}}`)
	}))
	defer upstream.Close()

	proxy := newFallbackProxy(t, upstream.URL, "big", "small")
	resp := postChat(t, proxy.URL, "", `{"model":"big"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	if got := resp.Header.Get("X-FireRouter-Model"); got != "big" {
		t.Fatalf("X-FireRouter-Model = %q, want big", got)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits %d, want 1", got)
	}
}

func TestNoFallbackAfterHeaders(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		mu.Lock()
		seen = append(seen, req.Model)
		mu.Unlock()
		if req.Model != "big" {
			t.Errorf("fallback %s was called after headers", req.Model)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_, _ = io.WriteString(w, "data: partial\n\n")
		_ = rc.Flush()
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("upstream cannot hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer upstream.Close()

	proxy := newFallbackProxy(t, upstream.URL, "big", "small")
	resp := postChat(t, proxy.URL, "", `{"model":"big","stream":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-FireRouter-Model"); got != "big" {
		t.Fatalf("X-FireRouter-Model = %q, want big", got)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "big" {
		t.Fatalf("upstream models = %v, want [big]", seen)
	}
}

func postChat(t *testing.T, serverURL, auth, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, serverURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func newFallbackProxy(t *testing.T, upstream, model, fallback string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(newFallbackHandler(t, upstream, model, fallback))
	t.Cleanup(s.Close)
	return s
}

func newFallbackHandler(t *testing.T, upstream, model, fallback string) *Handler {
	t.Helper()
	h, err := NewHandler(upstream, NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	h.SetRouter(router.New(router.File{Models: map[string]router.Model{
		model: {Fallbacks: []string{fallback}},
	}}))
	return h
}

func newTestProxy(t *testing.T, upstream string) *httptest.Server {
	t.Helper()
	h, err := NewHandler(upstream, NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}
