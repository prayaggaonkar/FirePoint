package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAdminRequiresToken(t *testing.T) {
	open := newAdminServer(t, "")
	resp := adminDo(t, http.MethodPost, open.URL+"/admin/fail", "", `{"model_contains":"primary","rate":1}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("open admin status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if status := chatStatus(t, open.URL, "primary-big"); status != http.StatusOK {
		t.Fatalf("unauthenticated fail changed the model, status %d", status)
	}

	locked := newAdminServer(t, "secret")
	for _, token := range []string{"", "wrong", "secret extra"} {
		resp = adminDo(t, http.MethodGet, locked.URL+"/admin/state", token, "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q status %d", token, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp = adminDo(t, http.MethodGet, locked.URL+"/admin/fail", "secret", "")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /admin/fail status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAdminFailResetAndState(t *testing.T) {
	ts := newAdminServer(t, "secret")
	state := postRule(t, ts, "secret", `{"model_contains":"primary","rate":1.0}`)
	if len(state.Rules) != 1 || state.Rules[0].ModelContains != "primary" || state.Rules[0].Rate != 1 {
		t.Fatalf("state after fail %+v", state.Rules)
	}
	if status := chatStatus(t, ts.URL, "primary-big"); status != http.StatusServiceUnavailable {
		t.Fatalf("primary status %d, want 503", status)
	}
	if status := chatStatus(t, ts.URL, "small"); status != http.StatusOK {
		t.Fatalf("unmatched model status %d, want 200", status)
	}
	if status := postStatus(t, ts.URL+"/v1/embeddings", `{"model":"primary","input":"hi"}`); status != http.StatusServiceUnavailable {
		t.Fatalf("embeddings status %d, want 503", status)
	}

	state = postRule(t, ts, "secret", `{"model_contains":"backup","rate":1}`)
	if len(state.Rules) != 2 || state.Rules[1].ModelContains != "backup" {
		t.Fatalf("second rule %+v", state.Rules)
	}
	// The same substring updates the rate instead of adding another rule.
	state = postRule(t, ts, "secret", `{"model_contains":"primary","rate":0}`)
	if len(state.Rules) != 2 || state.Rules[0].Rate != 0 {
		t.Fatalf("updated rules %+v", state.Rules)
	}
	if status := chatStatus(t, ts.URL, "primary-big"); status != http.StatusOK {
		t.Fatalf("rate 0 status %d, want 200", status)
	}
	if status := chatStatus(t, ts.URL, "backup-a"); status != http.StatusServiceUnavailable {
		t.Fatalf("backup status %d, want 503", status)
	}

	got := getState(t, ts, "secret")
	if len(got.Rules) != 2 {
		t.Fatalf("GET state %+v", got.Rules)
	}

	resp := adminDo(t, http.MethodPost, ts.URL+"/admin/reset", "secret", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if rules := getState(t, ts, "secret").Rules; len(rules) != 0 {
		t.Fatalf("rules after reset %+v", rules)
	}
	if status := chatStatus(t, ts.URL, "backup-a"); status != http.StatusOK {
		t.Fatalf("after reset status %d, want 200", status)
	}
}

func TestAdminRuleReplacesGlobalFailRate(t *testing.T) {
	h := newHandler(config{tokens: 1, tokenInterval: time.Millisecond, failRate: 1})
	h.setAdminToken("secret")
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	if status := chatStatus(t, ts.URL, "safe-model"); status != http.StatusServiceUnavailable {
		t.Fatalf("before rule status %d, want 503", status)
	}
	postRule(t, ts, "secret", `{"model_contains":"safe","rate":0}`)
	if status := chatStatus(t, ts.URL, "safe-model"); status != http.StatusOK {
		t.Fatalf("exempt model status %d, want 200", status)
	}
	if status := chatStatus(t, ts.URL, "other"); status != http.StatusServiceUnavailable {
		t.Fatalf("other model status %d, want 503", status)
	}
}

func TestAdminRejectsBadRule(t *testing.T) {
	ts := newAdminServer(t, "secret")
	for _, body := range []string{`{"rate":1}`, `{"model_contains":"m","rate":1.5}`, `{"model_contains":"m","rate":-0.1}`, `not-json`} {
		resp := adminDo(t, http.MethodPost, ts.URL+"/admin/fail", "secret", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s status %d", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if rules := getState(t, ts, "secret").Rules; len(rules) != 0 {
		t.Fatalf("bad rules were stored: %+v", rules)
	}
}

func TestAdminConcurrentUpdates(t *testing.T) {
	ts := newAdminServer(t, "secret")
	errCh := make(chan error, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := `{"model_contains":"m","rate":1}`
			if i%2 == 0 {
				body = `{"model_contains":"m","rate":0}`
			}
			if err := adminRound(ts.URL, "secret", body); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	resp := adminDo(t, http.MethodPost, ts.URL+"/admin/reset", "secret", "")
	resp.Body.Close()
	if rules := getState(t, ts, "secret").Rules; len(rules) != 0 {
		t.Fatalf("rules after concurrent reset %+v", rules)
	}
}

func adminRound(base, token, body string) error {
	for _, req := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/admin/fail", body},
		{http.MethodGet, "/admin/state", ""},
		{http.MethodPost, "/v1/chat/completions", `{"model":"m-model","stream":false}`},
	} {
		httpReq, err := http.NewRequest(req.method, base+req.path, strings.NewReader(req.body))
		if err != nil {
			return err
		}
		if req.body != "" {
			httpReq.Header.Set("Content-Type", "application/json")
		}
		if strings.HasPrefix(req.path, "/admin") {
			httpReq.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return nil
}

func newAdminServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	h := newHandler(config{tokens: 1, tokenInterval: time.Millisecond})
	h.setAdminToken(token)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func adminDo(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func postRule(t *testing.T, ts *httptest.Server, token, body string) adminState {
	t.Helper()
	resp := adminDo(t, http.MethodPost, ts.URL+"/admin/fail", token, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("fail status %d body %s", resp.StatusCode, raw)
	}
	var state adminState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func getState(t *testing.T, ts *httptest.Server, token string) adminState {
	t.Helper()
	resp := adminDo(t, http.MethodGet, ts.URL+"/admin/state", token, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("state status %d body %s", resp.StatusCode, raw)
	}
	var state adminState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func chatStatus(t *testing.T, base, model string) int {
	t.Helper()
	return postStatus(t, base+"/v1/chat/completions", `{"model":"`+model+`","stream":false}`)
}

func postStatus(t *testing.T, url, body string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
