package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
)

const (
	histMinUs = 1
	histMaxUs = 3_600_000_000 // one hour
	histSigs  = 3
	drainWait = 30 * time.Second
)

// requestBody is a fixed streaming chat request. It is not logged.
var requestBody = []byte(`{"model":"mock","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

// Config is one load run. Now and Ticks are set by tests; the command leaves
// Ticks nil so Run builds a ticker from Rate.
type Config struct {
	URL         string
	Rate        float64
	Duration    time.Duration
	Warmup      time.Duration
	Concurrency int
	Now         func() time.Time
	Ticks       <-chan time.Time
	Client      *http.Client
	// APIKey is sent as Authorization when set. It is not logged.
	APIKey string
	// Body is the request payload. Empty uses the default streaming chat.
	Body []byte
	// Split records the first part of the measured window separately from
	// the rest. Zero keeps a single window. Error classification is unchanged.
	Split time.Duration
}

// Percentiles is one latency distribution in milliseconds.
type Percentiles struct {
	P50  float64 `json:"p50"`
	P90  float64 `json:"p90"`
	P99  float64 `json:"p99"`
	P999 float64 `json:"p99.9"`
}

// Report is the measured window. Warmup attempts are not included.
type Report struct {
	URL             string      `json:"url"`
	Rate            float64     `json:"rate_per_sec"`
	DurationSeconds float64     `json:"duration_seconds"`
	WarmupSeconds   float64     `json:"warmup_seconds"`
	Concurrency     int         `json:"concurrency"`
	Requests        int         `json:"requests"`
	Errors          int         `json:"errors"`
	Status429       int         `json:"status_429"`
	TTFT            Percentiles `json:"ttft_ms"`
	InterToken      Percentiles `json:"inter_token_ms"`
	// First5s and Steady are set only when Split is positive. First5s is the
	// opening of the measured window. Steady is the remainder.
	SplitSeconds float64 `json:"split_seconds,omitempty"`
	First5s      *Window `json:"first_5s,omitempty"`
	Steady       *Window `json:"steady,omitempty"`
}

// Window is one slice of a measured run.
type Window struct {
	Requests   int         `json:"requests"`
	Errors     int         `json:"errors"`
	Status429  int         `json:"status_429"`
	TTFT       Percentiles `json:"ttft_ms"`
	InterToken Percentiles `json:"inter_token_ms"`
}

// Run sends streaming requests on a fixed schedule for Warmup+Duration.
// The ticker does not wait for a response. A tick that finds Concurrency
// requests already in flight is counted as an error instead of queueing,
// which would slow the schedule down to whatever the server can finish.
func Run(ctx context.Context, cfg Config) (Report, error) {
	if cfg.URL == "" || cfg.Rate <= 0 || cfg.Duration <= 0 || cfg.Concurrency < 1 || cfg.Warmup < 0 {
		return Report{}, errors.New("invalid loadgen config")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Client == nil {
		cfg.Client = newClient(cfg.Concurrency)
	}

	ttftHist := hdrhistogram.New(histMinUs, histMaxUs, histSigs)
	gapHist := hdrhistogram.New(histMinUs, histMaxUs, histSigs)
	rec := &recorder{ttft: ttftHist, gaps: gapHist}
	var earlyRec, steadyRec *recorder
	if cfg.Split > 0 {
		earlyRec = &recorder{ttft: hdrhistogram.New(histMinUs, histMaxUs, histSigs), gaps: hdrhistogram.New(histMinUs, histMaxUs, histSigs)}
		steadyRec = &recorder{ttft: hdrhistogram.New(histMinUs, histMaxUs, histSigs), gaps: hdrhistogram.New(histMinUs, histMaxUs, histSigs)}
	}

	start := cfg.Now()
	end := start.Add(cfg.Warmup + cfg.Duration)
	warmUntil := start.Add(cfg.Warmup)

	ticks := cfg.Ticks
	if ticks == nil {
		interval := time.Duration(float64(time.Second) / cfg.Rate)
		if interval <= 0 {
			interval = time.Nanosecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		ticks = ticker.C
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case tick, ok := <-ticks:
			if !ok {
				break loop
			}
			// The tick carries the schedule time. A real ticker’s value is
			// time.Now, and tests send their own clock so the loop does not
			// wait on a wall clock.
			if !tick.Before(end) {
				break loop
			}
			warmup := tick.Before(warmUntil)
			// Opening is the first Split of the measured window. The same
			// outcome is also stored on the full-window recorder.
			opening := earlyRec != nil && !warmup && tick.Before(warmUntil.Add(cfg.Split))
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func(discard, opening bool) {
					defer wg.Done()
					defer func() { <-sem }()
					out := doOne(reqCtx, cfg.Client, cfg.URL, cfg.APIKey, cfg.Body, cfg.Now)
					if discard {
						return
					}
					rec.add(out)
					if earlyRec == nil {
						return
					}
					if opening {
						earlyRec.add(out)
						return
					}
					steadyRec.add(out)
				}(warmup, opening)
			default:
				if !warmup {
					miss := outcome{err: errors.New("concurrency cap")}
					rec.add(miss)
					if earlyRec != nil {
						if opening {
							earlyRec.add(miss)
						} else {
							steadyRec.add(miss)
						}
					}
				}
			}
		}
	}

	stopped := make(chan struct{})
	go func() {
		timer := time.NewTimer(drainWait)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancel()
		case <-stopped:
		}
	}()
	wg.Wait()
	close(stopped)

	report := rec.report(cfg)
	if earlyRec != nil {
		opening := earlyRec.window()
		steady := steadyRec.window()
		report.SplitSeconds = cfg.Split.Seconds()
		report.First5s = &opening
		report.Steady = &steady
	}
	return report, nil
}

func newClient(concurrency int) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        concurrency,
			MaxIdleConnsPerHost: concurrency,
			DisableCompression:  true,
		},
	}
}

type outcome struct {
	status int
	ttft   time.Duration
	gaps   []time.Duration
	err    error
}

func doOne(ctx context.Context, client *http.Client, url, apiKey string, body []byte, now func() time.Time) outcome {
	if len(body) == 0 {
		body = requestBody
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return outcome{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	// TTFT starts as the request goes out, not when status bytes come back.
	sent := now()
	resp, err := client.Do(req)
	if err != nil {
		return outcome{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return outcome{status: http.StatusTooManyRequests}
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return outcome{status: resp.StatusCode, err: fmt.Errorf("status %d", resp.StatusCode)}
	}
	return readSSE(resp.Body, sent, now)
}

// readSSE times tokens from SSE data lines. HTTP headers and blank lines are
// not a token. [DONE] closes the stream and is not a token either.
func readSSE(r io.Reader, sent time.Time, now func() time.Time) outcome {
	br := bufio.NewReader(r)
	var prev time.Time
	var ttft time.Duration
	var gaps []time.Duration
	saw := false
	for {
		line, err := br.ReadString('\n')
		if payload, ok := dataPayload(line); ok && payload != "[DONE]" {
			if strings.HasPrefix(payload, `{"error"`) {
				// The proxy sends this when the upstream stream is cut. It is a
				// failed request, not a token.
				return outcome{status: http.StatusOK, ttft: ttft, gaps: gaps, err: errors.New("stream error event")}
			}
			at := now()
			if !saw {
				ttft = at.Sub(sent)
				saw = true
			} else if !prev.IsZero() {
				gaps = append(gaps, at.Sub(prev))
			}
			prev = at
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if !saw {
				return outcome{status: http.StatusOK, err: err}
			}
			return outcome{status: http.StatusOK, ttft: ttft, gaps: gaps, err: err}
		}
	}
	if !saw {
		return outcome{status: http.StatusOK, err: errors.New("no sse data")}
	}
	return outcome{status: http.StatusOK, ttft: ttft, gaps: gaps}
}

func dataPayload(line string) (string, bool) {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	return strings.TrimSpace(line[len("data:"):]), true
}

type recorder struct {
	mu       sync.Mutex
	ttft     *hdrhistogram.Histogram
	gaps     *hdrhistogram.Histogram
	requests int
	errors   int
	tooMany  int
}

func (r *recorder) add(out outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests++
	if out.status == http.StatusTooManyRequests {
		r.tooMany++
		return
	}
	if out.err != nil {
		r.errors++
		return
	}
	_ = r.ttft.RecordValue(histUs(out.ttft))
	for _, gap := range out.gaps {
		_ = r.gaps.RecordValue(histUs(gap))
	}
}

func (r *recorder) window() Window {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Window{
		Requests:   r.requests,
		Errors:     r.errors,
		Status429:  r.tooMany,
		TTFT:       percentiles(r.ttft),
		InterToken: percentiles(r.gaps),
	}
}

func (r *recorder) report(cfg Config) Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Report{
		URL:             cfg.URL,
		Rate:            cfg.Rate,
		DurationSeconds: cfg.Duration.Seconds(),
		WarmupSeconds:   cfg.Warmup.Seconds(),
		Concurrency:     cfg.Concurrency,
		Requests:        r.requests,
		Errors:          r.errors,
		Status429:       r.tooMany,
		TTFT:            percentiles(r.ttft),
		InterToken:      percentiles(r.gaps),
	}
}

func percentiles(h *hdrhistogram.Histogram) Percentiles {
	return Percentiles{
		P50:  usToMs(h.ValueAtPercentile(50)),
		P90:  usToMs(h.ValueAtPercentile(90)),
		P99:  usToMs(h.ValueAtPercentile(99)),
		P999: usToMs(h.ValueAtPercentile(99.9)),
	}
}

func histUs(d time.Duration) int64 {
	v := d.Microseconds()
	if v < histMinUs {
		return histMinUs
	}
	if v > histMaxUs {
		return histMaxUs
	}
	return v
}

func usToMs(us int64) float64 {
	return float64(us) / 1000
}

func printReport(w io.Writer, report Report) {
	fmt.Fprintf(w, "requests %d\nerrors %d\n429 %d\n", report.Requests, report.Errors, report.Status429)
	fmt.Fprintf(w, "TTFT ms        p50=%.3f p90=%.3f p99=%.3f p99.9=%.3f\n",
		report.TTFT.P50, report.TTFT.P90, report.TTFT.P99, report.TTFT.P999)
	fmt.Fprintf(w, "inter-token ms p50=%.3f p90=%.3f p99=%.3f p99.9=%.3f\n",
		report.InterToken.P50, report.InterToken.P90, report.InterToken.P99, report.InterToken.P999)
	if report.First5s != nil && report.Steady != nil {
		printWindow(w, fmt.Sprintf("first_%.0fs", report.SplitSeconds), *report.First5s)
		printWindow(w, "steady", *report.Steady)
	}
}

func printWindow(w io.Writer, label string, window Window) {
	fmt.Fprintf(w, "%s requests %d errors %d 429 %d\n", label, window.Requests, window.Errors, window.Status429)
	fmt.Fprintf(w, "%s TTFT ms        p50=%.3f p90=%.3f p99=%.3f p99.9=%.3f\n",
		label, window.TTFT.P50, window.TTFT.P90, window.TTFT.P99, window.TTFT.P999)
	fmt.Fprintf(w, "%s inter-token ms p50=%.3f p90=%.3f p99=%.3f p99.9=%.3f\n",
		label, window.InterToken.P50, window.InterToken.P90, window.InterToken.P99, window.InterToken.P999)
}
