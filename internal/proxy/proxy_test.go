package proxy

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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

func TestBodyAndAuthorizationPassThrough(t *testing.T) {
	const (
		wantBody = `{"model":"test","messages":[{"role":"user","content":"hi"}]}`
		wantAuth = "Bearer secret"
	)
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
		if got := r.Header.Get("Authorization"); got != wantAuth {
			t.Errorf("Authorization = %q, want %q", got, wantAuth)
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
	req.Header.Set("Authorization", wantAuth)
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
