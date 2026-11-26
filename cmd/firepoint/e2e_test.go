package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prayaggaonkar/firepoint/internal/auth"
)

// These tests build and start the real binaries, so flag parsing, signal
// handling, routing, and shutdown are exercised the way a user runs them.

type stack struct {
	proxyURL string
	mockURL  string
	router   *exec.Cmd
	keysPath string
	dir      string
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func build(t *testing.T, dir, pkg, name string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", out, pkg)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, b)
	}
	return out
}

func waitHealthy(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never became healthy", url)
}

func writeKeys(t *testing.T, path string, keys map[string]string) {
	t.Helper()
	var f auth.File
	for name, key := range keys {
		f.Tenants = append(f.Tenants, auth.Tenant{Name: name, KeySHA256: auth.HashKey(key), RPM: 600, TPM: 200000})
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func startStack(t *testing.T, keys map[string]string, mockArgs ...string) *stack {
	t.Helper()
	if testing.Short() {
		t.Skip("builds binaries")
	}
	dir := t.TempDir()
	routerBin := build(t, dir, ".", "firepoint")
	mockBin := build(t, dir, "../mockupstream", "mockupstream")

	s := &stack{dir: dir, keysPath: filepath.Join(dir, "keys.json")}
	mockAddr, proxyAddr := freeAddr(t), freeAddr(t)
	s.mockURL, s.proxyURL = "http://"+mockAddr, "http://"+proxyAddr
	writeKeys(t, s.keysPath, keys)
	cfgPath := filepath.Join(dir, "router.json")
	if err := os.WriteFile(cfgPath, []byte(`{"models":{"primary":{"fallbacks":["fallback"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	args := append([]string{"-addr", mockAddr, "-admin-token", "adm"}, mockArgs...)
	mock := exec.Command(mockBin, args...)
	if err := mock.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mock.Process.Kill(); _ = mock.Wait() })
	waitHealthy(t, s.mockURL)

	s.router = exec.Command(routerBin, "-listen", proxyAddr, "-upstream", s.mockURL, "-keys", s.keysPath, "-config", cfgPath)
	s.router.Stderr = os.Stderr
	if err := s.router.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.router.Process.Kill(); _ = s.router.Wait() })
	waitHealthy(t, s.proxyURL)
	return s
}

func (s *stack) chat(t *testing.T, key, model string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, model)
	req, err := http.NewRequest(http.MethodPost, s.proxyURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestE2EAuthStreamAndFallback(t *testing.T) {
	const key = "fp-e2e-alice-key"
	s := startStack(t, map[string]string{"alice": key}, "-first-token-delay", "1ms", "-token-interval", "1ms", "-tokens", "4")

	resp := s.chat(t, "", "primary")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no key: status %d, want 401", resp.StatusCode)
	}

	resp = s.chat(t, key, "primary")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-FirePoint-Model") != "primary" {
		t.Fatalf("status %d model %q", resp.StatusCode, resp.Header.Get("X-FirePoint-Model"))
	}
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("stream did not finish: %q", body)
	}

	req, _ := http.NewRequest(http.MethodPost, s.mockURL+"/admin/fail", strings.NewReader(`{"model_contains":"primary","rate":1}`))
	req.Header.Set("Authorization", "Bearer adm")
	r, err := http.DefaultClient.Do(req)
	if err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("admin fail: %v %v", err, r)
	}
	r.Body.Close()

	resp = s.chat(t, key, "primary")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-FirePoint-Model") != "fallback" {
		t.Fatalf("after failure: status %d model %q, want fallback", resp.StatusCode, resp.Header.Get("X-FirePoint-Model"))
	}
}

func TestE2ESighupReloadsKeys(t *testing.T) {
	const alice, bob = "fp-e2e-alice-key", "fp-e2e-bob-key"
	s := startStack(t, map[string]string{"alice": alice}, "-first-token-delay", "1ms", "-token-interval", "1ms", "-tokens", "2")

	resp := s.chat(t, bob, "primary")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bob before reload: %d", resp.StatusCode)
	}
	writeKeys(t, s.keysPath, map[string]string{"alice": alice, "bob": bob})
	if err := s.router.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp = s.chat(t, bob, "primary")
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bob still rejected after SIGHUP: %d", resp.StatusCode)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestE2EShutdownLetsInflightStreamFinish(t *testing.T) {
	const key = "fp-e2e-alice-key"
	s := startStack(t, map[string]string{"alice": key}, "-first-token-delay", "50ms", "-token-interval", "100ms", "-tokens", "8")

	resp := s.chat(t, key, "primary")
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	if _, err := rd.ReadString('\n'); err != nil {
		t.Fatalf("first line: %v", err)
	}
	if err := s.router.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("stream cut by shutdown: %v", err)
	}
	if !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("stream did not complete during drain: %q", rest)
	}
	done := make(chan error, 1)
	go func() { done <- s.router.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("router exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("router did not exit after draining")
	}
}
