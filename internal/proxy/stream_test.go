package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func BenchmarkStreamPump(b *testing.B) {
	payload := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	const chunks = 32
	src := &chunkReader{payload: payload}
	dst := discardWriter{header: make(http.Header)}
	stamp := time.Unix(0, 0)
	now := func() time.Time { return stamp }
	b.SetBytes(int64(chunks * len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src.left = chunks
		var mark byteTimes
		if _, err := streamBody(dst, src, &mark, now); err != nil {
			b.Fatal(err)
		}
	}
}

// chunkReader returns one SSE payload per Read, then EOF. A real upstream
// delivers tokens that way, so the pump flushes once per Read.
type chunkReader struct {
	payload []byte
	left    int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	r.left--
	return copy(p, r.payload), nil
}

type discardWriter struct {
	header http.Header
}

func (w discardWriter) Header() http.Header { return w.header }
func (w discardWriter) Write(p []byte) (int, error) {
	return len(p), nil
}
func (w discardWriter) WriteHeader(int) {}
func (w discardWriter) Flush()          {}

func TestCancelledStreamsReturnNearBaseline(t *testing.T) {
	const streams = 1000
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server starts watching for a closed client only after the
		// request body is consumed. Leaving it unread pins the handler
		// even though the proxy has already gone away.
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		if _, err := io.WriteString(w, "data: hi\n\n"); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()
	defer upstream.CloseClientConnections()

	// Keep-alives would leave idle connection goroutines after every stream
	// has ended. This test is about the streams, so connections close with them.
	closing := &http.Transport{DisableKeepAlives: true, DisableCompression: true}
	h, err := NewHandler(upstream.URL, &http.Client{Transport: closing}, "")
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(h)
	defer proxy.Close()
	defer proxy.CloseClientConnections()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DisableCompression: true}}

	if err := oneStream(client, proxy.URL); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	base := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var started, finished sync.WaitGroup
	started.Add(streams)
	finished.Add(streams)
	errs := make(chan error, streams)
	for i := 0; i < streams; i++ {
		go func() {
			defer finished.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true}`))
			if err != nil {
				errs <- err
				started.Done()
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				errs <- err
				started.Done()
				return
			}
			defer resp.Body.Close()
			buf := make([]byte, 16)
			if _, err := resp.Body.Read(buf); err != nil {
				errs <- err
				started.Done()
				return
			}
			started.Done()
			_, _ = io.Copy(io.Discard, resp.Body)
		}()
	}
	waitGroup(t, &started, 15*time.Second, "streams did not all start")
	cancel()
	waitGroup(t, &finished, 15*time.Second, "streams did not finish after cancel")
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// A stuck pump keeps a goroutine per stream. A handful of runtime or net
	// pollers can come and go between samples.
	const margin = 20
	deadline := time.Now().Add(time.Second)
	var got int
	for {
		time.Sleep(20 * time.Millisecond)
		got = runtime.NumGoroutine()
		if got <= base+margin {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines %d, baseline %d", got, base)
		}
	}
}

func oneStream(client *http.Client, proxyURL string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL+"/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	buf := make([]byte, 16)
	if _, err := resp.Body.Read(buf); err != nil {
		return err
	}
	cancel()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func waitGroup(t *testing.T, wg *sync.WaitGroup, limit time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatal(what)
	}
}
