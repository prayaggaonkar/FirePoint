package proxy

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

func newTestProxy(t *testing.T, upstream string) *httptest.Server {
	t.Helper()
	h, err := NewHandler(upstream, NewClient())
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}
