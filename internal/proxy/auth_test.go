package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/prayaggaonkar/firerouter/internal/auth"
)

func TestUnknownKeyNeverReachesUpstream(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	keys, err := auth.New(auth.File{Tenants: []auth.Tenant{{ID: "acme", Key: "acme-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(upstream.URL, NewClient(), "upstream-secret")
	if err != nil {
		t.Fatal(err)
	}
	h.SetKeys(keys)
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	resp := postChat(t, proxy.URL, "Bearer wrong", `{"model":"m","messages":[{"role":"user","content":"secret prompt"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if hits.Load() != 0 {
		t.Fatalf("upstream hits %d, want 0", hits.Load())
	}

	resp = postChat(t, proxy.URL, "Bearer acme-secret", `{"model":"m","stream":true}`)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("known key status %d", resp.StatusCode)
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hits %d, want 1", hits.Load())
	}
}
