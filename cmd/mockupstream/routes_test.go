package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prayaggaonkar/firepoint/internal/auth"
	"github.com/prayaggaonkar/firepoint/internal/proxy"
	"github.com/prayaggaonkar/firepoint/internal/router"
)

func TestProxyForwardsNonStreamingChat(t *testing.T) {
	const body = `{"model":"small","stream":false,"future_field":{"keep":true},"messages":[{"role":"user","content":"hi"}]}`
	var seen []byte
	upstream, proxyURL := proxyInFront(t, func(w http.ResponseWriter, r *http.Request) {
		buf, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}
		seen = append([]byte(nil), buf...)
		r.Body = io.NopCloser(bytes.NewReader(buf))
		w.Header().Set("Upgrade", "nope")
	}, config{tokens: 1})

	resp, err := http.Post(proxyURL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body %s", resp.StatusCode, got)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type %q", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("X-Request-Id") != "mock-req" {
		t.Fatalf("X-Request-Id %q", resp.Header.Get("X-Request-Id"))
	}
	if resp.Header.Get("Upgrade") != "" {
		t.Fatalf("hop-by-hop Upgrade was forwarded: %q", resp.Header.Get("Upgrade"))
	}
	if resp.Header.Get("X-FirePoint-Model") != "small" {
		t.Fatalf("model header %q", resp.Header.Get("X-FirePoint-Model"))
	}
	if string(seen) != body {
		t.Fatalf("upstream body = %s", seen)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Choices) != 1 || parsed.Choices[0].Message.Content != "t0" {
		t.Fatalf("choices = %+v", parsed.Choices)
	}
	_ = upstream
}

func TestProxyForwardsModelsEmbeddingsAndCompletions(t *testing.T) {
	_, proxyURL := proxyInFront(t, nil, config{tokens: 1})

	models, err := http.Get(proxyURL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer models.Body.Close()
	if models.StatusCode != http.StatusOK {
		t.Fatalf("models status %d", models.StatusCode)
	}
	if models.Header.Get("X-Request-Id") != "mock-req" {
		t.Fatalf("models X-Request-Id %q", models.Header.Get("X-Request-Id"))
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

	const embedBody = `{"model":"small","input":"hello","future_field":{"n":1}}`
	var embedSeen []byte
	embedProxy := proxyInFrontSeen(t, &embedSeen, config{tokens: 1})
	embed, err := http.Post(embedProxy+"/v1/embeddings", "application/json", strings.NewReader(embedBody))
	if err != nil {
		t.Fatal(err)
	}
	defer embed.Body.Close()
	if embed.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(embed.Body)
		t.Fatalf("embeddings status %d body %s", embed.StatusCode, got)
	}
	if string(embedSeen) != embedBody {
		t.Fatalf("embeddings upstream body = %s", embedSeen)
	}
	var emb struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(embed.Body).Decode(&emb); err != nil {
		t.Fatal(err)
	}
	if emb.Model != "small" || embed.Header.Get("X-FirePoint-Model") != "small" {
		t.Fatalf("embedding model %q header %q", emb.Model, embed.Header.Get("X-FirePoint-Model"))
	}

	const textBody = `{"model":"small","prompt":"hi","future_field":true}`
	var textSeen []byte
	textProxy := proxyInFrontSeen(t, &textSeen, config{tokens: 1})
	text, err := http.Post(textProxy+"/v1/completions", "application/json", strings.NewReader(textBody))
	if err != nil {
		t.Fatal(err)
	}
	defer text.Body.Close()
	if text.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(text.Body)
		t.Fatalf("completions status %d body %s", text.StatusCode, got)
	}
	if string(textSeen) != textBody {
		t.Fatalf("completions upstream body = %s", textSeen)
	}
	var comp struct {
		Choices []struct {
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(text.Body).Decode(&comp); err != nil {
		t.Fatal(err)
	}
	if len(comp.Choices) != 1 || comp.Choices[0].Text != "ok" {
		t.Fatalf("completion = %+v", comp.Choices)
	}
}

func TestEmbeddingFallbackAndTokenBudget(t *testing.T) {
	mock := newHandler(config{tokens: 1})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mock.ServeHTTP(w, r)
	}))
	t.Cleanup(upstream.Close)
	ph, err := proxy.NewHandler(upstream.URL, proxy.NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	ph.SetRouter(router.New(router.File{Models: map[string]router.Model{
		"big-broken": {Fallbacks: []string{"small"}},
	}}))
	ph.SetLimiter(100, 1)
	front := httptest.NewServer(ph)
	t.Cleanup(front.Close)

	resp, err := http.Post(front.URL+"/v1/embeddings", "application/json", strings.NewReader(`{"model":"big-broken","input":"hi","future_field":{"ok":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("fallback status %d body %s", resp.StatusCode, got)
	}
	if resp.Header.Get("X-FirePoint-Model") != "small" {
		t.Fatalf("header %q", resp.Header.Get("X-FirePoint-Model"))
	}

	// 8 input bytes is 2 tokens, and the budget allows 1.
	limited, err := http.Post(front.URL+"/v1/embeddings", "application/json", strings.NewReader(`{"model":"small","input":"12345678"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Body.Close()
	if limited.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("budget status %d, want 429", limited.StatusCode)
	}
}

func TestModelsRequireAKeyWhenConfigured(t *testing.T) {
	mock := newHandler(config{tokens: 1})
	upstream := httptest.NewServer(mock)
	t.Cleanup(upstream.Close)
	keys, err := auth.New(auth.File{Tenants: []auth.Tenant{{
		Name: "acme", KeySHA256: auth.HashKey("acme-secret"), RPM: 600, TPM: 200000,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ph, err := proxy.NewHandler(upstream.URL, proxy.NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	ph.SetKeys(keys)
	front := httptest.NewServer(ph)
	t.Cleanup(front.Close)

	resp, err := http.Get(front.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, front.URL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer acme-secret")
	ok, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("known key status %d", ok.StatusCode)
	}
}

func proxyInFront(t *testing.T, before func(http.ResponseWriter, *http.Request), cfg config) (*httptest.Server, string) {
	t.Helper()
	return proxyInFrontHook(t, before, nil, cfg)
}

func proxyInFrontSeen(t *testing.T, dst *[]byte, cfg config) string {
	t.Helper()
	_, url := proxyInFrontHook(t, nil, dst, cfg)
	return url
}

func proxyInFrontHook(t *testing.T, before func(http.ResponseWriter, *http.Request), dst *[]byte, cfg config) (*httptest.Server, string) {
	t.Helper()
	mock := newHandler(cfg)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if before != nil {
			before(w, r)
		}
		if dst != nil {
			buf, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read: %v", err)
				return
			}
			*dst = append([]byte(nil), buf...)
			r.Body = io.NopCloser(bytes.NewReader(buf))
		}
		mock.ServeHTTP(w, r)
	}))
	t.Cleanup(upstream.Close)
	ph, err := proxy.NewHandler(upstream.URL, proxy.NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(ph)
	t.Cleanup(front.Close)
	return upstream, front.URL
}
