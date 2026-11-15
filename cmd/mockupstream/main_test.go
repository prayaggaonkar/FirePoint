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

func TestBrokenModelNameReturns503(t *testing.T) {
	h := newHandler(config{
		firstTokenDelay: 0,
		tokenInterval:   time.Millisecond,
		tokens:          1,
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	broken, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"big-broken","stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Body.Close()
	if broken.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(broken.Body)
		t.Fatalf("status = %d, want 503, body %s", broken.StatusCode, body)
	}

	ok, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"small","stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(ok.Body)
		t.Fatalf("small status = %d, want 200, body %s", ok.StatusCode, body)
	}
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(ok.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Model != "small" {
		t.Fatalf("model = %q, want small", parsed.Model)
	}
}

func TestModelsEmbeddingsAndCompletions(t *testing.T) {
	h := newHandler(config{tokens: 1})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	models, err := http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer models.Body.Close()
	if models.StatusCode != http.StatusOK {
		t.Fatalf("models status %d", models.StatusCode)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(models.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != "mock" {
		t.Fatalf("models = %+v", list.Data)
	}

	embed, err := http.Post(ts.URL+"/v1/embeddings", "application/json", strings.NewReader(`{"model":"small","input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer embed.Body.Close()
	if embed.StatusCode != http.StatusOK {
		t.Fatalf("embeddings status %d", embed.StatusCode)
	}
	var emb struct {
		Model string `json:"model"`
		Data  []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(embed.Body).Decode(&emb); err != nil {
		t.Fatal(err)
	}
	if emb.Model != "small" || len(emb.Data) != 1 || len(emb.Data[0].Embedding) == 0 {
		t.Fatalf("embedding = %+v", emb)
	}

	broken, err := http.Post(ts.URL+"/v1/embeddings", "application/json", strings.NewReader(`{"model":"big-broken","input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Body.Close()
	if broken.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("broken embeddings status %d", broken.StatusCode)
	}

	text, err := http.Post(ts.URL+"/v1/completions", "application/json", strings.NewReader(`{"model":"small","prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer text.Body.Close()
	if text.StatusCode != http.StatusOK {
		t.Fatalf("completions status %d", text.StatusCode)
	}
	var comp struct {
		Model   string `json:"model"`
		Choices []struct {
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(text.Body).Decode(&comp); err != nil {
		t.Fatal(err)
	}
	if comp.Model != "small" || len(comp.Choices) != 1 || comp.Choices[0].Text == "" {
		t.Fatalf("completion = %+v", comp)
	}
}

func lastOrEmpty(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}
