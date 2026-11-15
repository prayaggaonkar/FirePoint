package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prayaggaonkar/firerouter/internal/auth"
)

func TestValidKey(t *testing.T) {
	const fireworks = "fw-real-key"
	var hits atomic.Int32
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	key, err := auth.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(upstream.URL, NewClient(), fireworks)
	if err != nil {
		t.Fatal(err)
	}
	h.SetKeys(storeFor(t, "alice", key, nil))
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	resp := postChat(t, proxy.URL, "Bearer "+key, `{"model":"m","messages":[{"role":"user","content":"secret prompt"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hits %d", hits.Load())
	}
	if gotAuth != "Bearer "+fireworks {
		t.Fatalf("upstream Authorization = %q", gotAuth)
	}
	if strings.Contains(gotAuth, key) {
		t.Fatal("virtual key was forwarded")
	}
}

func TestBadKey(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	key, err := auth.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(upstream.URL, NewClient(), "fw-real-key")
	if err != nil {
		t.Fatal(err)
	}
	h.SetKeys(storeFor(t, "alice", key, nil))
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	other, err := auth.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"Bearer " + other, ""} {
		resp := postChat(t, proxy.URL, header, `{"model":"m","messages":[{"role":"user","content":"secret prompt"}]}`)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("header %q status %d body %s", header, resp.StatusCode, body)
		}
		var apiErr struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &apiErr); err != nil {
			t.Fatal(err)
		}
		if apiErr.Error.Type != "invalid_api_key" || apiErr.Error.Message == "" {
			t.Fatalf("error %#v", apiErr.Error)
		}
		if strings.Contains(apiErr.Error.Message, key) || strings.Contains(string(body), "secret prompt") {
			t.Fatalf("error leaked a secret: %s", body)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("upstream hits %d, want 0", hits.Load())
	}
}

func TestModelNotAllowed(t *testing.T) {
	var hits atomic.Int32
	var sawModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sawModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	key, err := auth.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(upstream.URL, NewClient(), "fw-real-key")
	if err != nil {
		t.Fatal(err)
	}
	h.SetKeys(storeFor(t, "alice", key, []string{"small"}))
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	resp := postChat(t, proxy.URL, "Bearer "+key, `{"model":"big","messages":[{"role":"user","content":"hi"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	var apiErr struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &apiErr); err != nil {
		t.Fatal(err)
	}
	if apiErr.Error.Type != "invalid_request_error" || apiErr.Error.Code != "model_not_allowed" {
		t.Fatalf("error %#v", apiErr.Error)
	}
	if hits.Load() != 0 {
		t.Fatalf("disallowed model reached upstream %d times", hits.Load())
	}

	resp = postChat(t, proxy.URL, "Bearer "+key, `{"model":"small"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed model status %d", resp.StatusCode)
	}
	if sawModel != "small" || hits.Load() != 1 {
		t.Fatalf("upstream model %q hits %d", sawModel, hits.Load())
	}
}

func TestReloadKeepsInFlightStream(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_ = rc.Flush()
		close(started)
		<-release
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		_ = rc.Flush()
	}))
	defer upstream.Close()

	oldKey, err := auth.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := auth.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	writeKeys(t, path, "alice", oldKey)

	h, err := NewHandler(upstream.URL, NewClient(), "fw-real-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ReloadKeys(path); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	type result struct {
		status int
		body   string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
		if err != nil {
			done <- result{err: err}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+oldKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		done <- result{status: resp.StatusCode, body: string(body), err: err}
	}()

	select {
	case <-started:
	case res := <-done:
		t.Fatalf("stream ended early: %+v", res)
	}
	// Swap the file while the first response is still open. The stream
	// already passed auth, so it must finish on the old key.
	writeKeys(t, path, "bob", newKey)
	if err := h.ReloadKeys(path); err != nil {
		t.Fatal(err)
	}
	close(release)

	res := <-done
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.status != http.StatusOK || !strings.Contains(res.body, "hi") || !strings.Contains(res.body, "[DONE]") {
		t.Fatalf("in-flight stream status %d body %s", res.status, res.body)
	}

	old := postChat(t, proxy.URL, "Bearer "+oldKey, `{"model":"m"}`)
	old.Body.Close()
	if old.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old key status %d, want 401", old.StatusCode)
	}
	next := postChat(t, proxy.URL, "Bearer "+newKey, `{"model":"m"}`)
	next.Body.Close()
	if next.StatusCode != http.StatusOK {
		t.Fatalf("new key status %d", next.StatusCode)
	}
}

func TestReloadKeepsPreviousKeysWhenFileIsBad(t *testing.T) {
	key, err := auth.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	writeKeys(t, path, "alice", key)
	h, err := NewHandler("http://127.0.0.1", NewClient(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ReloadKeys(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.ReloadKeys(path); err == nil {
		t.Fatal("bad file was accepted")
	}
	who, ok := h.authenticate("Bearer " + key)
	if !ok || who.name != "alice" {
		t.Fatalf("previous key was dropped: %+v ok=%v", who, ok)
	}
}

func storeFor(t *testing.T, name, key string, models []string) *auth.Store {
	t.Helper()
	s, err := auth.New(auth.File{Tenants: []auth.Tenant{{
		Name:          name,
		KeySHA256:     auth.HashKey(key),
		RPM:           600,
		TPM:           200000,
		AllowedModels: models,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func writeKeys(t *testing.T, path, name, key string) {
	t.Helper()
	body, err := json.Marshal(auth.File{Tenants: []auth.Tenant{{
		Name: name, KeySHA256: auth.HashKey(key), RPM: 600, TPM: 200000,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}
