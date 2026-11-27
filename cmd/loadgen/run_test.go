package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
)

func TestTTFTStartsAtTheFirstDataLine(t *testing.T) {
	const gap = 50 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		// A blank line is not a token. TTFT has to include the wait below.
		_, _ = io.WriteString(w, "\n")
		_ = rc.Flush()
		time.Sleep(gap)
		_, _ = io.WriteString(w, "data: one\n\n")
		_ = rc.Flush()
		time.Sleep(gap)
		_, _ = io.WriteString(w, "data: two\n\n")
		_ = rc.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		_ = rc.Flush()
	}))
	t.Cleanup(srv.Close)

	out := doOne(context.Background(), srv.Client(), srv.URL, "", nil, time.Now)
	if out.err != nil {
		t.Fatal(out.err)
	}
	if out.ttft < gap/2 {
		t.Fatalf("ttft %s stopped at the first body byte", out.ttft)
	}
	if len(out.gaps) != 1 {
		t.Fatalf("gaps = %d, want 1", len(out.gaps))
	}
	if out.gaps[0] < gap/2 {
		t.Fatalf("inter-token gap %s", out.gaps[0])
	}
}

func TestOpenLoopKeepsFiringWhileBusy(t *testing.T) {
	release := make(chan struct{})
	var inflight atomic.Int32
	var maxIn atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		for {
			old := maxIn.Load()
			if n <= old || maxIn.CompareAndSwap(old, n) {
				break
			}
		}
		defer inflight.Add(-1)
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: t\n\n")
	}))
	t.Cleanup(srv.Close)

	start := time.Unix(1_700_000_000, 0)
	clk := &manualClock{t: start}
	ticks := make(chan time.Time)
	done := make(chan Report, 1)
	errCh := make(chan error, 1)
	go func() {
		report, err := Run(context.Background(), Config{
			URL:         srv.URL,
			Rate:        100,
			Duration:    time.Second,
			Concurrency: 1,
			Now:         clk.Now,
			Ticks:       ticks,
			Client:      srv.Client(),
		})
		if err != nil {
			errCh <- err
			return
		}
		done <- report
	}()

	ticks <- start
	for i := 0; i < 4; i++ {
		ticks <- start.Add(time.Duration(i+1) * time.Millisecond)
	}
	ticks <- start.Add(time.Second)
	close(release)

	select {
	case err := <-errCh:
		t.Fatal(err)
	case report := <-done:
		if report.Errors != 4 || report.ConcurrencyCap != 4 {
			t.Fatalf("errors = %d cap = %d, want 4 cap misses while one request was in flight", report.Errors, report.ConcurrencyCap)
		}
		if report.Requests != 5 {
			t.Fatalf("requests = %d, want 5", report.Requests)
		}
		if got := maxIn.Load(); got != 1 {
			t.Fatalf("max in flight = %d, want 1", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
	}
}

func TestWarmupResultsAreDiscarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: t\n\n")
	}))
	t.Cleanup(srv.Close)

	start := time.Unix(1_700_000_000, 0)
	clk := &manualClock{t: start}
	ticks := make(chan time.Time)
	done := make(chan Report, 1)
	go func() {
		report, err := Run(context.Background(), Config{
			URL:         srv.URL,
			Rate:        1,
			Duration:    10 * time.Second,
			Warmup:      10 * time.Second,
			Concurrency: 2,
			Now:         clk.Now,
			Ticks:       ticks,
			Client:      srv.Client(),
		})
		if err != nil {
			t.Errorf("run: %v", err)
			return
		}
		done <- report
	}()

	ticks <- start.Add(time.Second)
	ticks <- start.Add(11 * time.Second)
	ticks <- start.Add(20 * time.Second)
	select {
	case report := <-done:
		if report.Requests != 1 {
			t.Fatalf("requests = %d, want 1 measured request", report.Requests)
		}
		if report.Errors != 0 || report.Status429 != 0 {
			t.Fatalf("errors %d 429 %d", report.Errors, report.Status429)
		}
		if report.TTFT.P50 <= 0 {
			t.Fatal("missing ttft sample")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
	}
}

func TestStatus429IsCountedApartFromErrors(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: t\n\n")
	}))
	t.Cleanup(srv.Close)

	start := time.Unix(1_700_000_000, 0)
	clk := &manualClock{t: start}
	ticks := make(chan time.Time)
	done := make(chan Report, 1)
	go func() {
		report, _ := Run(context.Background(), Config{
			URL:         srv.URL,
			Rate:        1,
			Duration:    time.Second,
			Concurrency: 2,
			Now:         clk.Now,
			Ticks:       ticks,
			Client:      srv.Client(),
		})
		done <- report
	}()
	ticks <- start
	ticks <- start.Add(time.Millisecond)
	ticks <- start.Add(time.Second)

	select {
	case report := <-done:
		if report.Status429 != 1 || report.Errors != 0 {
			t.Fatalf("429 %d errors %d", report.Status429, report.Errors)
		}
		if report.StatusCodes["429"] != 1 || report.StatusCodes["200"] != 1 {
			t.Fatalf("status codes %v", report.StatusCodes)
		}
		if report.Requests != 2 {
			t.Fatalf("requests %d", report.Requests)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
	}
}

func TestReportJSON(t *testing.T) {
	report := Report{
		URL: "http://example", Rate: 2, DurationSeconds: 1, WarmupSeconds: 10,
		Concurrency: 4, Requests: 3, Errors: 1, Status429: 1,
		TTFT:       Percentiles{P50: 1, P90: 2, P99: 3, P999: 4},
		InterToken: Percentiles{P50: 5, P90: 6, P99: 7, P999: 8},
	}
	path := filepath.Join(t.TempDir(), "loadgen.json")
	if err := writeJSON(path, report); err != nil {
		t.Fatal(err)
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status429 != 1 || got.TTFT.P999 != 4 || got.InterToken.P50 != 5 {
		t.Fatalf("json = %+v", got)
	}
}

func TestDoOneSendsKeyAndModel(t *testing.T) {
	var gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body chatBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		gotModel = body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: hi\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	payload, err := json.Marshal(chatBody{Model: "primary", Stream: true, Messages: []chatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	out := doOne(context.Background(), srv.Client(), srv.URL, "fp-test", payload, time.Now)
	if out.err != nil {
		t.Fatal(out.err)
	}
	if gotAuth != "Bearer fp-test" {
		t.Fatalf("authorization %q", gotAuth)
	}
	if gotModel != "primary" {
		t.Fatalf("model %q", gotModel)
	}
}

func TestPercentilesFromHistogram(t *testing.T) {
	h := hdrhistogram.New(histMinUs, histMaxUs, histSigs)
	for i := 0; i < 100; i++ {
		if err := h.RecordValue(2_000); err != nil {
			t.Fatal(err)
		}
	}
	got := percentiles(h)
	if got.P50 < 1.5 || got.P50 > 2.5 || got.P999 < 1.5 || got.P999 > 2.5 {
		t.Fatalf("percentiles = %+v, want about 2 ms", got)
	}
}

type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func TestDoOneCountsServerErrorsAndDeadConnections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	out := doOne(context.Background(), srv.Client(), srv.URL, "", nil, time.Now)
	if out.err == nil || out.status != http.StatusServiceUnavailable {
		t.Fatalf("503: status %d err %v", out.status, out.err)
	}
	srv.Close()
	out = doOne(context.Background(), srv.Client(), srv.URL, "", nil, time.Now)
	if out.err == nil {
		t.Fatal("a refused connection was not an error")
	}
}

func TestStreamCutAndErrorEventAreErrors(t *testing.T) {
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	defer cut.Close()
	if out := doOne(context.Background(), cut.Client(), cut.URL, "", nil, time.Now); out.err == nil {
		t.Fatal("a truncated stream was counted as success")
	}

	errEvent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\ndata: {\"error\":{\"message\":\"x\"}}\n\n")
	}))
	defer errEvent.Close()
	if out := doOne(context.Background(), errEvent.Client(), errEvent.URL, "", nil, time.Now); out.err == nil {
		t.Fatal("an error event was counted as success")
	}
}

func TestSplitKeepsTheFullWindowAndTheSlices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: t\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	start := time.Unix(1_700_000_000, 0)
	clk := &manualClock{t: start}
	ticks := make(chan time.Time)
	done := make(chan Report, 1)
	go func() {
		report, err := Run(context.Background(), Config{
			URL:         srv.URL,
			Rate:        1,
			Warmup:      10 * time.Millisecond,
			Split:       10 * time.Millisecond,
			Duration:    30 * time.Millisecond,
			Concurrency: 4,
			Now:         clk.Now,
			Ticks:       ticks,
			Client:      srv.Client(),
		})
		if err != nil {
			t.Error(err)
			return
		}
		done <- report
	}()
	ticks <- start
	ticks <- start.Add(10 * time.Millisecond)
	ticks <- start.Add(20 * time.Millisecond)
	ticks <- start.Add(30 * time.Millisecond)
	ticks <- start.Add(40 * time.Millisecond)
	close(ticks)

	select {
	case report := <-done:
		if report.Requests != 3 || report.Errors != 0 {
			t.Fatalf("full window requests=%d errors=%d, want 3 and 0", report.Requests, report.Errors)
		}
		if report.First5s == nil || report.First5s.Requests != 1 || report.First5s.Errors != 0 {
			t.Fatalf("opening = %+v", report.First5s)
		}
		if report.Steady == nil || report.Steady.Requests != 2 || report.Steady.Errors != 0 {
			t.Fatalf("steady = %+v", report.Steady)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
	}
}

func TestNoSSEDataIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer srv.Close()
	if out := doOne(context.Background(), srv.Client(), srv.URL, "", nil, time.Now); out.err == nil {
		t.Fatal("an empty stream was counted as success")
	}
}
