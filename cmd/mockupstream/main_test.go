package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamEventsArriveOverTime(t *testing.T) {
	const (
		interval = 50 * time.Millisecond
		tokens   = 4
	)
	// first-token-delay is zero so the measured gap comes from token-interval.
	h := newHandler(config{
		firstTokenDelay: 0,
		tokenInterval:   interval,
		tokens:          tokens,
		failRate:        0,
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Time each data line as it is read. A buffered response would deliver
	// every line in the same instant; a flushed stream blocks between tokens.
	reader := bufio.NewReader(resp.Body)
	var data []string
	var arrivals []time.Time
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		data = append(data, payload)
		arrivals = append(arrivals, time.Now())
		if payload == "[DONE]" {
			break
		}
	}
	if len(data) == 0 || data[len(data)-1] != "[DONE]" {
		t.Fatalf("last data line = %q, want [DONE]", lastOrEmpty(data))
	}
	span := arrivals[len(arrivals)-1].Sub(arrivals[0])
	minSpan := time.Duration(tokens-1) * interval / 2
	if span < minSpan {
		t.Fatalf("events arrived together in %s, want at least %s", span, minSpan)
	}
}

func TestFailRateReturns503(t *testing.T) {
	h := newHandler(config{
		firstTokenDelay: 0,
		tokenInterval:   time.Millisecond,
		tokens:          1,
		failRate:        1,
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 503, body %s", resp.StatusCode, body)
	}
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Error.Message == "" || parsed.Error.Type == "" {
		t.Fatalf("error body = %+v, want an OpenAI-style error object", parsed.Error)
	}
}

func lastOrEmpty(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}
